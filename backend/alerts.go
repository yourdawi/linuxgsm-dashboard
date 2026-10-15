package backend

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type AlertSettings struct {
	DiscordEnabled    bool   `json:"discord_enabled"`
	DiscordWebhook    string `json:"discord_webhook"`
	TelegramEnabled   bool   `json:"telegram_enabled"`
	TelegramToken     string `json:"telegram_token"`
	TelegramChatID    string `json:"telegram_chatid"`
	EmailEnabled      bool   `json:"email_enabled"`
	EmailSMTP         string `json:"email_smtp"`
	EmailPort         string `json:"email_port"`
	EmailUser         string `json:"email_user"`
	EmailPass         string `json:"email_pass"`
	EmailDest         string `json:"email_dest"`
	MatrixEnabled     bool   `json:"matrix_enabled"`
	MatrixHomeserver  string `json:"matrix_homeserver"`
	MatrixRoomID      string `json:"matrix_roomid"`
	MatrixAccessToken string `json:"matrix_accesstoken"`
	NtfyEnabled       bool   `json:"ntfy_enabled"`
	NtfyURL           string `json:"ntfy_url"`
	NtfyTopic         string `json:"ntfy_topic"`
	NtfyAuthToken     string `json:"ntfy_authtoken"`
	SlackEnabled      bool   `json:"slack_enabled"`
	SlackWebhook      string `json:"slack_webhook"`
	PushoverEnabled   bool   `json:"pushover_enabled"`
	PushoverToken     string `json:"pushover_token"`
	PushoverUser      string `json:"pushover_user"`
	PushbulletEnabled bool   `json:"pushbullet_enabled"`
	PushbulletToken   string `json:"pushbullet_token"`
	PushbulletChannel string `json:"pushbullet_channel"`
	IftttEnabled      bool   `json:"ifttt_enabled"`
	IftttKey          string `json:"ifttt_key"`
	IftttEvent        string `json:"ifttt_event"`
	RocketchatEnabled bool   `json:"rocketchat_enabled"`
	RocketchatWebhook string `json:"rocketchat_webhook"`
}

// SecretMask replaces stored secrets in API responses. Submitting it back keeps the stored value.
const SecretMask = "********"

// secretFields returns pointers to every secret value of the settings.
func (a *AlertSettings) secretFields() []*string {
	return []*string{
		&a.DiscordWebhook, &a.TelegramToken, &a.EmailPass, &a.MatrixAccessToken, &a.NtfyAuthToken,
		&a.SlackWebhook, &a.PushoverToken, &a.PushbulletToken, &a.IftttKey, &a.RocketchatWebhook,
	}
}

// allValues returns pointers to every string value (used for validation).
func (a *AlertSettings) allValues() []*string {
	return []*string{
		&a.DiscordWebhook, &a.TelegramToken, &a.TelegramChatID, &a.EmailSMTP, &a.EmailPort, &a.EmailUser,
		&a.EmailPass, &a.EmailDest, &a.MatrixHomeserver, &a.MatrixRoomID, &a.MatrixAccessToken, &a.NtfyURL,
		&a.NtfyTopic, &a.NtfyAuthToken, &a.SlackWebhook, &a.PushoverToken, &a.PushoverUser,
		&a.PushbulletToken, &a.PushbulletChannel, &a.IftttKey, &a.IftttEvent, &a.RocketchatWebhook,
	}
}

// Masked returns a copy with all non-empty secrets replaced by SecretMask.
func (a AlertSettings) Masked() AlertSettings {
	for _, f := range a.secretFields() {
		if *f != "" {
			*f = SecretMask
		}
	}
	return a
}

// mergeSecrets keeps stored secrets for fields submitted as SecretMask.
func (a *AlertSettings) mergeSecrets(stored AlertSettings) {
	newFields := a.secretFields()
	oldFields := stored.secretFields()
	for i := range newFields {
		if *newFields[i] == SecretMask {
			*newFields[i] = *oldFields[i]
		}
	}
}

// Validate rejects values that cannot be stored safely in a bash-sourced LinuxGSM config.
func (a *AlertSettings) Validate() error {
	for _, v := range a.allValues() {
		if !IsSafeCfgValue(*v) {
			return fmt.Errorf("invalid characters in alert settings (quotes, $, `, \\ and line breaks are not allowed)")
		}
	}
	return nil
}

var reAlertSetting = regexp.MustCompile(`^\s*([a-zA-Z0-9]+)\s*=\s*["']?([^"'\s#]*)["']?`)

func alertConfigRels(srv GameServerInstance) (common, rootCommon, instance, rootInstance string) {
	instRel, rootRel := configDirsRel(srv)
	return filepath.Join(instRel, "common.cfg"), filepath.Join(rootRel, "common.cfg"),
		filepath.Join(instRel, srv.Script+".cfg"), filepath.Join(rootRel, srv.Script+".cfg")
}

func (im *InstanceManager) loadAlertSettings(srv GameServerInstance) AlertSettings {
	commonRel, rootCommonRel, instanceRel, rootInstanceRel := alertConfigRels(srv)
	settings := AlertSettings{}

	parseFile := func(rel string) {
		content, err := readServerCfg(srv, rel)
		if err != nil {
			return
		}
		for _, line := range strings.Split(content, "\n") {
			matches := reAlertSetting.FindStringSubmatch(line)
			if len(matches) <= 2 {
				continue
			}
			key := strings.ToLower(matches[1])
			val := matches[2]
			switch key {
			case "discordalert":
				settings.DiscordEnabled = (val == "on")
			case "discordwebhook":
				settings.DiscordWebhook = val
			case "telegramalert":
				settings.TelegramEnabled = (val == "on")
			case "telegramtoken":
				settings.TelegramToken = val
			case "telegramchatid":
				settings.TelegramChatID = val
			case "emailalert":
				settings.EmailEnabled = (val == "on")
			case "emailserver":
				settings.EmailSMTP = val
			case "emailport":
				settings.EmailPort = val
			case "emailuser":
				settings.EmailUser = val
			case "emailpassword":
				settings.EmailPass = val
			case "emaildest":
				settings.EmailDest = val
			case "matrixalert":
				settings.MatrixEnabled = (val == "on")
			case "matrixhomeserver":
				settings.MatrixHomeserver = val
			case "matrixroomid":
				settings.MatrixRoomID = val
			case "matrixaccesstoken":
				settings.MatrixAccessToken = val
			case "ntfyalert":
				settings.NtfyEnabled = (val == "on")
			case "ntfyurl":
				settings.NtfyURL = val
			case "ntfytopic":
				settings.NtfyTopic = val
			case "ntfyauthtoken":
				settings.NtfyAuthToken = val
			case "slackalert":
				settings.SlackEnabled = (val == "on")
			case "slackwebhook":
				settings.SlackWebhook = val
			case "pushoveralert":
				settings.PushoverEnabled = (val == "on")
			case "pushovertoken":
				settings.PushoverToken = val
			case "pushoveruser":
				settings.PushoverUser = val
			case "pushbulletalert":
				settings.PushbulletEnabled = (val == "on")
			case "pushbullettoken":
				settings.PushbulletToken = val
			case "pushbulletchannel":
				settings.PushbulletChannel = val
			case "iftttalert":
				settings.IftttEnabled = (val == "on")
			case "iftttkey":
				settings.IftttKey = val
			case "iftttevent":
				settings.IftttEvent = val
			case "rocketchatalert":
				settings.RocketchatEnabled = (val == "on")
			case "rocketchatwebhook":
				settings.RocketchatWebhook = val
			}
		}
	}

	parseFile(rootCommonRel)
	parseFile(commonRel)
	parseFile(rootInstanceRel)
	parseFile(instanceRel)
	return settings
}

// GetAlertSettings returns the alert configuration with all secrets masked.
func (im *InstanceManager) GetAlertSettings(serverID string) (AlertSettings, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return AlertSettings{}, fmt.Errorf("server not found")
	}
	return im.loadAlertSettings(srv).Masked(), nil
}

func (im *InstanceManager) SaveAlertSettings(serverID string, settings AlertSettings) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}

	settings.mergeSecrets(im.loadAlertSettings(srv))
	if err := settings.Validate(); err != nil {
		return err
	}

	instRel, rootRel := configDirsRel(srv)
	instancePaths := []string{filepath.Join(instRel, srv.Script+".cfg")}
	if rootRel != instRel {
		instancePaths = append(instancePaths, filepath.Join(rootRel, srv.Script+".cfg"))
	}

	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}

	order := []string{
		"discordalert", "discordwebhook", "telegramalert", "telegramtoken", "telegramchatid",
		"emailalert", "emailserver", "emailport", "emailuser", "emailpassword", "emaildest",
		"matrixalert", "matrixhomeserver", "matrixroomid", "matrixaccesstoken",
		"ntfyalert", "ntfyurl", "ntfytopic", "ntfyauthtoken", "slackalert", "slackwebhook",
		"pushoveralert", "pushovertoken", "pushoveruser", "pushbulletalert", "pushbullettoken",
		"pushbulletchannel", "iftttalert", "iftttkey", "iftttevent", "rocketchatalert", "rocketchatwebhook",
	}
	valMap := map[string]string{
		"discordalert":      onOff(settings.DiscordEnabled),
		"discordwebhook":    settings.DiscordWebhook,
		"telegramalert":     onOff(settings.TelegramEnabled),
		"telegramtoken":     settings.TelegramToken,
		"telegramchatid":    settings.TelegramChatID,
		"emailalert":        onOff(settings.EmailEnabled),
		"emailserver":       settings.EmailSMTP,
		"emailport":         settings.EmailPort,
		"emailuser":         settings.EmailUser,
		"emailpassword":     settings.EmailPass,
		"emaildest":         settings.EmailDest,
		"matrixalert":       onOff(settings.MatrixEnabled),
		"matrixhomeserver":  settings.MatrixHomeserver,
		"matrixroomid":      settings.MatrixRoomID,
		"matrixaccesstoken": settings.MatrixAccessToken,
		"ntfyalert":         onOff(settings.NtfyEnabled),
		"ntfyurl":           settings.NtfyURL,
		"ntfytopic":         settings.NtfyTopic,
		"ntfyauthtoken":     settings.NtfyAuthToken,
		"slackalert":        onOff(settings.SlackEnabled),
		"slackwebhook":      settings.SlackWebhook,
		"pushoveralert":     onOff(settings.PushoverEnabled),
		"pushovertoken":     settings.PushoverToken,
		"pushoveruser":      settings.PushoverUser,
		"pushbulletalert":   onOff(settings.PushbulletEnabled),
		"pushbullettoken":   settings.PushbulletToken,
		"pushbulletchannel": settings.PushbulletChannel,
		"iftttalert":        onOff(settings.IftttEnabled),
		"iftttkey":          settings.IftttKey,
		"iftttevent":        settings.IftttEvent,
		"rocketchatalert":   onOff(settings.RocketchatEnabled),
		"rocketchatwebhook": settings.RocketchatWebhook,
	}

	for _, rel := range instancePaths {
		content, err := readServerCfg(srv, rel)
		if err != nil {
			return fmt.Errorf("failed to read config")
		}
		newContent := setCfgValues(content, valMap, order, true)
		if err := writeServerFile(srv, rel, []byte(newContent)); err != nil {
			return fmt.Errorf("failed to write config")
		}
	}

	return nil
}

// -------------------------------------------------------------
// Stop Mode Configurator
// -------------------------------------------------------------

// IsValidStopMode validates LinuxGSM stopmode values offered by the UI.
func IsValidStopMode(mode string) bool {
	return mode == "" || mode == "default" || isIntInRange(mode, 1, 13)
}

var reStopModeValue = regexp.MustCompile(`^\s*stopmode\s*=\s*["']?([^"'\s#]*)["']?`)
var reStopModeKey = regexp.MustCompile(`^\s*stopmode\s*=`)

func (im *InstanceManager) GetStopModeSettings(serverID string) (string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return "", fmt.Errorf("server not found")
	}

	commonRel, rootCommonRel, instanceRel, rootInstanceRel := alertConfigRels(srv)

	stopMode := "default"
	parseFile := func(rel string) {
		content, err := readServerCfg(srv, rel)
		if err != nil {
			return
		}
		for _, line := range strings.Split(content, "\n") {
			matches := reStopModeValue.FindStringSubmatch(line)
			if len(matches) > 1 && matches[1] != "" {
				stopMode = matches[1]
			}
		}
	}

	parseFile(rootCommonRel)
	parseFile(commonRel)
	parseFile(rootInstanceRel)
	parseFile(instanceRel)
	return stopMode, nil
}

func (im *InstanceManager) SaveStopModeSettings(serverID string, stopMode string) error {
	srv, ok := im.getServer(serverID)
	if !ok {
		return fmt.Errorf("server not found")
	}
	if !IsValidStopMode(stopMode) {
		return fmt.Errorf("invalid stop mode")
	}

	instRel, rootRel := configDirsRel(srv)
	instancePaths := []string{filepath.Join(instRel, srv.Script+".cfg")}
	if rootRel != instRel {
		instancePaths = append(instancePaths, filepath.Join(rootRel, srv.Script+".cfg"))
	}

	for _, rel := range instancePaths {
		content, err := readServerCfg(srv, rel)
		if err != nil {
			return fmt.Errorf("failed to read config")
		}

		lines := strings.Split(content, "\n")
		updated := false
		for i, line := range lines {
			if reStopModeKey.MatchString(line) {
				if stopMode == "default" || stopMode == "" {
					lines[i] = "# stopmode=\"\""
				} else {
					lines[i] = fmt.Sprintf("stopmode=\"%s\"", stopMode)
				}
				updated = true
				break
			}
		}

		if !updated && stopMode != "default" && stopMode != "" {
			lines = append(lines, fmt.Sprintf("stopmode=\"%s\"", stopMode))
		}

		if err := writeServerFile(srv, rel, []byte(strings.Join(lines, "\n"))); err != nil {
			return fmt.Errorf("failed to write config")
		}
	}

	return nil
}
