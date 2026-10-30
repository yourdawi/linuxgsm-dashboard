package backend

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Alert settings are LinuxGSM variables (see the "Notification Alerts" part of _default.cfg and
// lgsm/modules/alert_*.sh). The dashboard edits them by their LinuxGSM names; the browser gets the
// values the user set, LinuxGSM's defaults as placeholders and the file each value comes from.

// AlertField is one LinuxGSM variable of the alert settings.
type AlertField struct {
	Key    string `json:"key"`
	Secret bool   `json:"secret,omitempty"` // masked in answers, stored in secrets-<script>.cfg
	Switch bool   `json:"switch,omitempty"` // on/off
}

// AlertService groups the variables of one service; the first field is its on/off switch.
type AlertService struct {
	ID     string       `json:"id"`
	Fields []AlertField `json:"fields"`
}

func onOffField(key string) AlertField { return AlertField{Key: key, Switch: true} }
func textField(key string) AlertField  { return AlertField{Key: key} }
func secretField(key string) AlertField {
	return AlertField{Key: key, Secret: true}
}

// AlertServices lists what LinuxGSM supports, "general" first.
var AlertServices = []AlertService{
	{"general", []AlertField{onOffField("statusalert"), onOffField("postalert"), textField("displayip")}},
	{"discord", []AlertField{onOffField("discordalert"), secretField("discordwebhook")}},
	{"email", []AlertField{onOffField("emailalert"), textField("email"), textField("emailfrom")}},
	{"gotify", []AlertField{onOffField("gotifyalert"), secretField("gotifytoken"), secretField("gotifywebhook")}},
	{"ifttt", []AlertField{onOffField("iftttalert"), secretField("ifttttoken"), textField("iftttevent")}},
	{"matrix", []AlertField{onOffField("matrixalert"), textField("matrixhomeserver"), secretField("matrixtoken"), textField("matrixroom")}},
	{"ntfy", []AlertField{onOffField("ntfyalert"), textField("ntfyserver"), textField("ntfytopic"), secretField("ntfytoken"),
		textField("ntfyusername"), secretField("ntfypassword"), textField("ntfypriority"), textField("ntfytags")}},
	{"pushbullet", []AlertField{onOffField("pushbulletalert"), secretField("pushbullettoken"), textField("channeltag")}},
	{"pushover", []AlertField{onOffField("pushoveralert"), secretField("pushovertoken"), secretField("pushoveruserkey")}},
	{"rocketchat", []AlertField{onOffField("rocketchatalert"), secretField("rocketchatwebhook")}},
	{"slack", []AlertField{onOffField("slackalert"), secretField("slackwebhook")}},
	{"telegram", []AlertField{onOffField("telegramalert"), secretField("telegramtoken"), textField("telegramchatid"),
		textField("telegramthreadid"), textField("telegramapi")}},
}

// legacyAlertKeys are names earlier dashboard versions wrote; LinuxGSM never read them. Their
// values are taken over once and the lines are removed on the next save.
var legacyAlertKeys = map[string]string{
	"emaildest": "email", "matrixroomid": "matrixroom", "matrixaccesstoken": "matrixtoken",
	"ntfyurl": "ntfyserver", "ntfyauthtoken": "ntfytoken", "pushoveruser": "pushoveruserkey",
	"iftttkey": "ifttttoken", "pushbulletchannel": "channeltag",
	"emailserver": "", "emailport": "", "emailuser": "", "emailpassword": "",
}

func alertField(key string) (AlertField, bool) {
	for _, s := range AlertServices {
		for _, f := range s.Fields {
			if f.Key == key {
				return f, true
			}
		}
	}
	return AlertField{}, false
}

// SecretMask replaces stored secrets in API responses. Submitting it back keeps the stored value.
const SecretMask = "********"

// AlertSettings is the answer of GET .../alerts.
type AlertSettings struct {
	Services []AlertService    `json:"services"`
	Values   map[string]string `json:"values"`   // set by the user (secrets masked)
	Defaults map[string]string `json:"defaults"` // LinuxGSM defaults (_default.cfg), not for secrets
	Sources  map[string]string `json:"sources"`  // file a value comes from, e.g. common.cfg
}

// alertState reads the user-set values (with legacy keys mapped) and where they come from.
// legacy holds the keys whose value was found under an old dashboard name.
func alertState(srv GameServerInstance) (values, sources, defaults map[string]string, legacy map[string]bool) {
	values, sources, defaults, legacy = map[string]string{}, map[string]string{}, map[string]string{}, map[string]bool{}
	files := lgsmCfgFiles(srv, true)
	for i, rel := range files {
		content, err := readServerCfg(srv, rel)
		if err != nil {
			continue
		}
		for k, v := range cfgValues(content) {
			if i == 0 {
				defaults[k] = v
				continue
			}
			key := k
			if newKey, isLegacy := legacyAlertKeys[k]; isLegacy {
				if newKey == "" {
					continue
				}
				if _, set := sources[newKey]; set {
					continue // the real variable wins
				}
				key = newKey
			}
			if _, ok := alertField(key); !ok {
				continue
			}
			values[key] = v
			sources[key] = filepath.Base(rel)
			legacy[key] = key != k
		}
	}
	return values, sources, defaults, legacy
}

// GetAlertSettings returns the alert configuration with all secrets masked.
func (im *InstanceManager) GetAlertSettings(serverID string) (AlertSettings, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return AlertSettings{}, fmt.Errorf("server not found")
	}
	values, sources, defaults, _ := alertState(srv)
	out := AlertSettings{Services: AlertServices, Values: map[string]string{}, Defaults: map[string]string{}, Sources: sources}
	for key, v := range values {
		if f, _ := alertField(key); f.Secret && v != "" {
			v = SecretMask
		}
		out.Values[key] = v
	}
	for _, s := range AlertServices {
		for _, f := range s.Fields {
			if d, ok := defaults[f.Key]; ok && !f.Secret {
				out.Defaults[f.Key] = d
			}
		}
	}
	return out, nil
}

// SaveAlertSettings applies submitted values. Only keys whose value changed are written, so
// values the user did not touch (also ones with $ or quotes) stay as they are. Secrets go to
// secrets-<script>.cfg, everything else to <script>.cfg; a cleared field removes the instance
// value, so common.cfg or LinuxGSM's default applies again.
func (im *InstanceManager) SaveAlertSettings(serverID string, submitted map[string]string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	current, sources, defaults, legacy := alertState(srv)
	// Values to rewrite although unchanged: found under an old name, or a secret in the plain
	// instance config (earlier versions put them there).
	move := func(key string, f AlertField) bool {
		return legacy[key] || f.Secret && sources[key] == srv.Script+".cfg"
	}

	plain, secret := map[string]*string{}, map[string]*string{}
	for key, v := range submitted {
		f, known := alertField(key)
		if !known {
			return fmt.Errorf("unknown alert setting %q", key)
		}
		if f.Secret && v == SecretMask {
			if !move(key, f) {
				continue
			}
			v = current[key]
		}
		if f.Switch && v != "on" && v != "off" {
			return fmt.Errorf("%s must be on or off", key)
		}
		if strings.ContainsAny(v, "\r\n\x00") || len(v) > 2048 {
			return fmt.Errorf("invalid value for %s", key)
		}
		// Not set by the user and equal to LinuxGSM's default: nothing to write, so later changes of
		// the default (or of common.cfg) still apply.
		if _, set := current[key]; !set && (v == "" || v == defaults[key]) {
			continue
		}
		// An empty field always clears the instance value (earlier versions wrote key="").
		if old, set := current[key]; set && old == v && v != "" && !move(key, f) {
			continue
		}
		val := v
		if f.Secret {
			secret[key] = &val
		} else {
			plain[key] = &val
		}
	}

	dir := srv.lgsmConfigRel()
	instanceRel := filepath.Join(dir, srv.Script+".cfg")
	secretsRel := filepath.Join(dir, "secrets-"+srv.Script+".cfg")
	instance, err := readServerCfg(srv, instanceRel)
	if err != nil {
		return err
	}
	secrets, err := readServerCfg(srv, secretsRel)
	if err != nil {
		return err
	}
	secretsKeys := cfgValues(secrets)

	instChanges, secChanges := map[string]*string{}, map[string]*string{}
	for old := range legacyAlertKeys {
		instChanges[old], secChanges[old] = nil, nil
	}
	for key, v := range plain {
		if _, inSecrets := secretsKeys[key]; inSecrets {
			// Someone keeps it in the secrets file, which is loaded last: change it there.
			secChanges[key] = v
			instChanges[key] = nil
		} else {
			instChanges[key] = v
		}
	}
	for key, v := range secret {
		// Plain copies written by earlier versions are removed.
		secChanges[key], instChanges[key] = v, nil
	}
	clear := func(m map[string]*string) {
		for k, v := range m {
			if v != nil && *v == "" {
				m[k] = nil
			}
		}
	}
	clear(instChanges)
	clear(secChanges)

	if updated := applyCfgChanges(instance, instChanges); updated != instance {
		if err := writeServerFilePerm(srv, instanceRel, []byte(updated), 0644); err != nil {
			return fmt.Errorf("failed to write config")
		}
	}
	if updated := applyCfgChanges(secrets, secChanges); updated != secrets {
		if err := writeServerFilePerm(srv, secretsRel, []byte(updated), 0600); err != nil {
			return fmt.Errorf("failed to write config")
		}
	}
	return nil
}

// applyCfgChanges sets (non-nil) or removes (nil) keys of a bash config. Set values are written
// double-quoted with \, ", $ and ` escaped, so bash reads them literally; new keys are appended.
func applyCfgChanges(content string, changes map[string]*string) string {
	if len(changes) == 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines)+len(changes))
	done := map[string]bool{}
	for _, line := range lines {
		m := reCfgAssignment.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			out = append(out, line)
			continue
		}
		v, ok := changes[m[1]]
		if !ok {
			out = append(out, line)
			continue
		}
		if v == nil || done[m[1]] {
			continue
		}
		done[m[1]] = true
		out = append(out, m[1]+"="+bashDoubleQuote(*v))
	}
	var add []string
	for k, v := range changes {
		if v != nil && !done[k] {
			add = append(add, k+"="+bashDoubleQuote(*v))
		}
	}
	if len(add) == 0 {
		return strings.Join(out, "\n")
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	sort.Strings(add)
	return strings.Join(append(out, add...), "\n") + "\n"
}

// bashDoubleQuote quotes s for a bash assignment so that it is taken literally.
func bashDoubleQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")
	return `"` + r.Replace(s) + `"`
}

// -------------------------------------------------------------
// Stop Mode Configurator
// -------------------------------------------------------------

// IsValidStopMode validates LinuxGSM stopmode values (fn_stop_graceful_select knows 1-16).
func IsValidStopMode(mode string) bool {
	return mode == "" || mode == "default" || isIntInRange(mode, 1, 16)
}

// GetStopModeSettings returns the stop mode set by the user, or "default" when only the game's
// _default.cfg sets it.
func (im *InstanceManager) GetStopModeSettings(serverID string) (string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return "", fmt.Errorf("server not found")
	}
	if mode := lgsmCfgValues(srv, false)["stopmode"]; mode != "" {
		return mode, nil
	}
	return "default", nil
}

func (im *InstanceManager) SaveStopModeSettings(serverID string, stopMode string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if !IsValidStopMode(stopMode) {
		return fmt.Errorf("invalid stop mode")
	}

	if stopMode == "default" {
		stopMode = "" // removes the override
	}
	if err := saveInstanceCfgValues(srv, map[string]string{"stopmode": stopMode}, []string{"stopmode"}, false); err != nil {
		return fmt.Errorf("failed to write config")
	}
	return nil
}
