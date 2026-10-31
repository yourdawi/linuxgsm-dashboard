package backend

import (
	"os"
	"path/filepath"
	"strings"
)

// LinuxGSM sources its configs as bash files from config-lgsm/<gameservername> in this order,
// later files overriding earlier ones: _default.cfg, common.cfg, secrets-common.cfg,
// <script>.cfg, secrets-<script>.cfg.

// lgsmCfgFiles returns the config files of srv in LinuxGSM's load order, relative to srv.Dir.
func lgsmCfgFiles(srv GameServerInstance, includeDefault bool) []string {
	dir := srv.lgsmConfigRel()
	var files []string
	if includeDefault {
		files = append(files, filepath.Join(dir, "_default.cfg"))
	}
	return append(files,
		filepath.Join(dir, "common.cfg"),
		filepath.Join(dir, "secrets-common.cfg"),
		filepath.Join(dir, srv.Script+".cfg"),
		filepath.Join(dir, "secrets-"+srv.Script+".cfg"),
	)
}

// cfgValue returns the value of the right-hand side of a bash assignment: the text inside
// quotes, or an unquoted word up to whitespace or a comment.
func cfgValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	switch raw[0] {
	case '"':
		var b strings.Builder
		for i := 1; i < len(raw); i++ {
			c := raw[i]
			if c == '\\' && i+1 < len(raw) && strings.ContainsRune(`"\$`+"`", rune(raw[i+1])) {
				b.WriteByte(raw[i+1])
				i++
				continue
			}
			if c == '"' {
				break
			}
			b.WriteByte(c)
		}
		return b.String()
	case '\'':
		if end := strings.IndexByte(raw[1:], '\''); end >= 0 {
			return raw[1 : end+1]
		}
		return raw[1:]
	}
	if i := strings.IndexAny(raw, " \t#;"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// cfgValues returns the values of all key=value lines of a config, the last assignment winning.
func cfgValues(content string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		if m := reCfgAssignment.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			values[m[1]] = cfgValue(m[2])
		}
	}
	return values
}

// lgsmCfgValues merges the LinuxGSM configs of srv like LinuxGSM does. Without includeDefault,
// only values set by the user (common, instance and secrets files) are returned.
func lgsmCfgValues(srv GameServerInstance, includeDefault bool) map[string]string {
	merged := make(map[string]string)
	for _, rel := range lgsmCfgFiles(srv, includeDefault) {
		content, err := readServerCfg(srv, rel)
		if err != nil {
			continue
		}
		for k, v := range cfgValues(content) {
			merged[k] = v
		}
	}
	return merged
}

// lgsmCfgValuesAt merges the configs of script (in homeDir) below configLgsm (lgsm/config-lgsm), including
// _default.cfg. It is used during discovery, before a GameServerInstance exists; only regular
// files are read, so a FIFO or device planted by a game server user cannot block the dashboard.
func lgsmCfgValuesAt(homeDir, configLgsm, script string) map[string]string {
	dir := filepath.Join(configLgsm, gameServerNameOf(homeDir, script))
	merged := make(map[string]string)
	for _, name := range []string{"_default.cfg", "common.cfg", "secrets-common.cfg", script + ".cfg", "secrets-" + script + ".cfg"} {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxEditableFileSize {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for k, v := range cfgValues(string(data)) {
			merged[k] = v
		}
	}
	return merged
}

// saveInstanceCfgValues writes key="value" lines into the instance config <script>.cfg. Keys that
// secrets-<script>.cfg also sets are updated there too, because that file is loaded last. An
// empty value removes the key unless keepEmpty is set, so the LinuxGSM default applies again.
// All values must already be validated with IsSafeCfgValue.
func saveInstanceCfgValues(srv GameServerInstance, values map[string]string, order []string, keepEmpty bool) error {
	dir := srv.lgsmConfigRel()
	instanceRel := filepath.Join(dir, srv.Script+".cfg")
	content, err := readServerCfg(srv, instanceRel)
	if err != nil {
		return err
	}
	if err := writeServerFile(srv, instanceRel, []byte(updateCfgLines(content, values, order, keepEmpty, true))); err != nil {
		return err
	}

	secretsRel := filepath.Join(dir, "secrets-"+srv.Script+".cfg")
	secrets, err := readServerCfg(srv, secretsRel)
	if err != nil || secrets == "" {
		return nil
	}
	if updated := updateCfgLines(secrets, values, order, keepEmpty, false); updated != secrets {
		return writeServerFile(srv, secretsRel, []byte(updated))
	}
	return nil
}

// updateCfgLines replaces the assignments of the given keys. Missing keys are appended (in order)
// when appendMissing is set; empty values remove the line unless keepEmpty is set.
func updateCfgLines(content string, values map[string]string, order []string, keepEmpty, appendMissing bool) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines)+len(order))
	seen := make(map[string]bool)
	for _, line := range lines {
		m := reCfgAssignment.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			out = append(out, line)
			continue
		}
		val, ok := values[m[1]]
		if !ok {
			out = append(out, line)
			continue
		}
		if seen[m[1]] || (val == "" && !keepEmpty) {
			continue // drop duplicates and cleared values
		}
		seen[m[1]] = true
		out = append(out, m[1]+`="`+val+`"`)
	}
	if appendMissing {
		for len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		for _, key := range order {
			if val, ok := values[key]; ok && !seen[key] && (val != "" || keepEmpty) {
				out = append(out, key+`="`+val+`"`)
			}
		}
		return strings.Join(out, "\n") + "\n"
	}
	return strings.Join(out, "\n")
}
