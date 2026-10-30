package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configNames(t *testing.T, im *InstanceManager, id string) map[string]string {
	t.Helper()
	files, err := im.GetConfigFiles(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Name] = f.Layer
	}
	return out
}

func TestConfigFilesIncludeLinuxGSMGameConfig(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "lgsm", "config-lgsm", "arkserver")
	mustWrite(t, filepath.Join(cfgDir, "_default.cfg"), "systemdir=\"${serverfiles}/ShooterGame\"\n"+
		"servercfgdir=\"${systemdir}/Saved/Config/LinuxServer\"\nservercfg=\"GameUserSettings.ini\"\n"+
		"servercfgfullpath=\"${servercfgdir}/${servercfg}\"\n")
	sf := filepath.Join(dir, "serverfiles")
	mustWrite(t, filepath.Join(sf, "ShooterGame", "Saved", "Config", "LinuxServer", "GameUserSettings.ini"), "[ServerSettings]\n")
	mustWrite(t, filepath.Join(sf, "ShooterGame", "Saved", "SaveGames", "x.ini"), "save data\n")
	mustWrite(t, filepath.Join(sf, "ops.json"), "[]\n")
	mustWrite(t, filepath.Join(sf, "world", "stats", "u.json"), "{}\n")
	mustWrite(t, filepath.Join(sf, "a", "b", "deep.json"), "{}\n")
	mustWrite(t, filepath.Join(sf, "plugins", "x", "config.yml"), "a: 1\n")
	srv := GameServerInstance{ID: "arkserver", Script: "arkserver", User: "ark", Dir: dir}
	im := NewStaticInstanceManager(t.TempDir(), srv)

	names := configNames(t, im, "arkserver")
	if names["serverfiles/ShooterGame/Saved/Config/LinuxServer/GameUserSettings.ini"] != "game" {
		t.Errorf("game config missing or not marked: %v", names)
	}
	for _, want := range []string{"serverfiles/ops.json", "serverfiles/plugins/x/config.yml"} {
		if _, ok := names[want]; !ok {
			t.Errorf("%s not listed: %v", want, names)
		}
	}
	for _, unwanted := range []string{"serverfiles/world/stats/u.json", "serverfiles/a/b/deep.json", "serverfiles/ShooterGame/Saved/SaveGames/x.ini"} {
		if _, ok := names[unwanted]; ok {
			t.Errorf("%s listed", unwanted)
		}
	}
}

func TestConfigFileInHomeWithOwnExtension(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "lgsm", "config-lgsm", "pzserver")
	// HOME is the server directory here (the user is not in /etc/passwd of the test machine).
	mustWrite(t, filepath.Join(cfgDir, "_default.cfg"), "servercfgdir=\"${HOME}/Zomboid/Server\"\nservercfg=\"${selfname}.sii\"\n"+
		"servercfgfullpath=\"${servercfgdir}/${servercfg}\"\nbroken=\"$(id)\"\n")
	cfg := filepath.Join(dir, "Zomboid", "Server", "pzserver.sii")
	mustWrite(t, cfg, "PVP=true\n")
	srv := GameServerInstance{ID: "pzserver", Script: "pzserver", User: "nobody-here", Dir: dir}
	im := NewStaticInstanceManager(t.TempDir(), srv)

	if names := configNames(t, im, "pzserver"); names["Zomboid/Server/pzserver.sii"] != "game" {
		t.Fatalf("game config in $HOME not listed: %v", names)
	}
	if content, err := im.GetConfigFileContent("pzserver", cfg); err != nil || content != "PVP=true\n" {
		t.Fatalf("read: %q %v", content, err)
	}
	if _, err := im.SaveConfigFileContent("pzserver", cfg, "PVP=false\n"); err != nil {
		t.Fatal(err)
	}
	// Other files in $HOME stay closed.
	other := filepath.Join(dir, "Zomboid", "Server", "other.sii")
	mustWrite(t, other, "x")
	if _, err := im.GetConfigFileContent("pzserver", other); err == nil {
		t.Error("a file that is not LinuxGSM's config was opened")
	}
}

func TestExpandLGSMValue(t *testing.T) {
	base := map[string]string{"rootdir": "/home/vh", "serverfiles": "/home/vh/serverfiles", "selfname": "vhserver"}
	values := map[string]string{"systemdir": "${serverfiles}/game", "servercfgdir": "$systemdir/cfg"}
	if got, ok := expandLGSMValue("${servercfgdir}/${selfname}.cfg", values, base); !ok || got != "/home/vh/serverfiles/game/cfg/vhserver.cfg" {
		t.Errorf("got %q %v", got, ok)
	}
	for _, bad := range []string{"${unknown}/x", "$(id)/x", "`id`", "${loop}"} {
		if _, ok := expandLGSMValue(bad, map[string]string{"loop": "${loop}"}, base); ok {
			t.Errorf("expanded %q", bad)
		}
	}
}

func TestGameServerNameFromScript(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "rust-eu"), "#!/bin/bash\n# LinuxGSM\nshortname=\"rust\"\ngameservername=\"rustserver\"\n")
	mustWrite(t, filepath.Join(dir, "vhserver-2"), "#!/bin/bash\n# no name here\n")
	if got := gameServerNameOf(dir, "rust-eu"); got != "rustserver" {
		t.Errorf("rust-eu -> %q", got)
	}
	if got := gameServerNameOf(dir, "vhserver-2"); got != "vhserver" {
		t.Errorf("fallback -> %q", got)
	}
	srv := GameServerInstance{Script: "rust-eu", Dir: dir}
	if rel := srv.lgsmConfigRel(); !strings.HasSuffix(filepath.ToSlash(rel), "config-lgsm/rustserver") {
		t.Errorf("config dir %q", rel)
	}
	_ = os.Remove(filepath.Join(dir, "rust-eu"))
}
