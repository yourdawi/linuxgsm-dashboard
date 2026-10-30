package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlertSettingsUseLinuxGSMVariables(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "lgsm", "config-lgsm", "vhserver")
	mustWrite(t, filepath.Join(cfgDir, "_default.cfg"), "ntfytopic=\"LinuxGSM\"\nntfyserver=\"https://ntfy.sh\"\ndiscordwebhook=\"webhook\"\nslackalert=\"off\"\n")
	mustWrite(t, filepath.Join(cfgDir, "common.cfg"), "matrixhomeserver=\"matrix.example\"\n")
	// Written by an earlier dashboard version: wrong names, secrets in plain text, empty values.
	mustWrite(t, filepath.Join(cfgDir, "vhserver.cfg"), "port=\"2456\"\nmatrixroomid=\"!room:x\"\nmatrixaccesstoken=\"tok\"\n"+
		"ntfytopic=\"\"\nemailserver=\"smtp\"\ndiscordwebhook=\"https://hook/secret\"\n")
	srv := GameServerInstance{ID: "vhserver", User: "vhserver", Script: "vhserver", Dir: dir}
	im := NewStaticInstanceManager(t.TempDir(), srv)

	got, err := im.GetAlertSettings("vhserver")
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["matrixroom"] != "!room:x" || got.Values["matrixtoken"] != SecretMask || got.Values["discordwebhook"] != SecretMask {
		t.Fatalf("values %v", got.Values)
	}
	if got.Defaults["ntfytopic"] != "LinuxGSM" || got.Defaults["discordwebhook"] != "" {
		t.Fatalf("defaults %v (secrets must not be shown)", got.Defaults)
	}
	if got.Sources["matrixhomeserver"] != "common.cfg" {
		t.Fatalf("sources %v", got.Sources)
	}

	// The browser sends everything back, secrets masked, and enables ntfy.
	submit := map[string]string{}
	for k, v := range got.Values {
		submit[k] = v
	}
	submit["ntfyalert"] = "on"
	submit["slackalert"] = "off" // the default: not written
	submit["ntfytopic"] = ""     // cleared: LinuxGSM's default applies
	submit["email"] = `ops+$USER@example.com "x"`
	if err := im.SaveAlertSettings("vhserver", submit); err != nil {
		t.Fatal(err)
	}
	inst, _ := os.ReadFile(filepath.Join(cfgDir, "vhserver.cfg"))
	secrets, _ := os.ReadFile(filepath.Join(cfgDir, "secrets-vhserver.cfg"))
	for _, gone := range []string{"matrixroomid", "slackalert", "matrixaccesstoken", "emailserver", "ntfytopic", "discordwebhook", "matrixhomeserver"} {
		if strings.Contains(string(inst), gone) {
			t.Errorf("instance config still has %s:\n%s", gone, inst)
		}
	}
	for _, want := range []string{`port="2456"`, `matrixroom="!room:x"`, `ntfyalert="on"`, `email="ops+\$USER@example.com \"x\""`} {
		if !strings.Contains(string(inst), want) {
			t.Errorf("instance config misses %s:\n%s", want, inst)
		}
	}
	for _, want := range []string{`matrixtoken="tok"`, `discordwebhook="https://hook/secret"`} {
		if !strings.Contains(string(secrets), want) {
			t.Errorf("secrets misses %s:\n%s", want, secrets)
		}
	}
	if info, err := os.Stat(filepath.Join(cfgDir, "secrets-vhserver.cfg")); err == nil && info.Mode().Perm() != 0600 {
		t.Errorf("secrets file mode %v", info.Mode().Perm())
	}

	// bash reads the escaped value literally.
	if _, err := exec.LookPath("bash"); err == nil {
		out, err := exec.Command("bash", "-c", "source \"$1\"; printf %s \"$email\"", "x", filepath.Join(cfgDir, "vhserver.cfg")).Output()
		if err != nil || string(out) != `ops+$USER@example.com "x"` {
			t.Errorf("bash reads %q (%v)", out, err)
		}
	}

	// Unchanged values are not touched, even when they could not be written by the form.
	mustWrite(t, filepath.Join(cfgDir, "vhserver.cfg"), "telegramchatid=\"${chat}\"\n")
	got, _ = im.GetAlertSettings("vhserver")
	if err := im.SaveAlertSettings("vhserver", got.Values); err != nil {
		t.Fatal(err)
	}
	if inst, _ := os.ReadFile(filepath.Join(cfgDir, "vhserver.cfg")); string(inst) != "telegramchatid=\"${chat}\"\n" {
		t.Errorf("unchanged value rewritten: %q", inst)
	}

	for _, bad := range []map[string]string{{"nope": "x"}, {"slackalert": "maybe"}, {"email": "a\nrm -rf ~"}} {
		if err := im.SaveAlertSettings("vhserver", bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// Every variable the dashboard writes must exist in LinuxGSM's _default.cfg.
func TestAlertFieldsExistInLinuxGSM(t *testing.T) {
	path := os.Getenv("LGSM_DEFAULT_CFG")
	if path == "" {
		t.Skip("set LGSM_DEFAULT_CFG to a LinuxGSM _default.cfg")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	known := cfgValues(string(data))
	for _, s := range AlertServices {
		for _, f := range s.Fields {
			if _, ok := known[f.Key]; !ok {
				t.Errorf("%s is no LinuxGSM variable", f.Key)
			}
		}
	}
}
