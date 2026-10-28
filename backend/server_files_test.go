package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveDefaultCfgWritesInstanceConfig(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "lgsm", "config-lgsm", "cssserver")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	defaultCfg := "## Default\nip=\"0.0.0.0\"\nport=\"27015\"\ndefaultmap=\"de_dust2\"\nmaxplayers=\"16\"\n"
	defaultPath := filepath.Join(cfgDir, "_default.cfg")
	instPath := filepath.Join(cfgDir, "cssserver.cfg")
	if err := os.WriteFile(defaultPath, []byte(defaultCfg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instPath, []byte("# instance\nmaxplayers=\"20\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	im := NewStaticInstanceManager(t.TempDir(), GameServerInstance{ID: "css", Dir: dir, Script: "cssserver"})

	edited := "## Default\nip=\"0.0.0.0\"\nport=\"27016\"\ndefaultmap=\"de_dust2\"\nmaxplayers=\"24\"\n"
	res, err := im.SaveConfigFileContent("css", defaultPath, edited)
	if err != nil {
		t.Fatal(err)
	}
	if res.RedirectedTo != instPath || !reflect.DeepEqual(res.Keys, []string{"port", "maxplayers"}) {
		t.Errorf("unexpected result %+v", res)
	}
	if data, _ := os.ReadFile(defaultPath); string(data) != defaultCfg {
		t.Errorf("_default.cfg was modified: %q", data)
	}
	want := "# instance\nmaxplayers=\"24\"\nport=\"27016\"\n"
	if data, _ := os.ReadFile(instPath); string(data) != want {
		t.Errorf("instance config = %q, want %q", data, want)
	}

	// Saving the unchanged defaults touches nothing.
	res, err = im.SaveConfigFileContent("css", defaultPath, defaultCfg)
	if err != nil || len(res.Keys) != 0 || res.RedirectedTo == "" {
		t.Errorf("unchanged save: %+v %v", res, err)
	}
	if data, _ := os.ReadFile(instPath); string(data) != want {
		t.Errorf("instance config changed by a no-op save: %q", data)
	}
}

func TestSaveDefaultCfgCreatesInstanceConfig(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "lgsm", "config-lgsm", "ut2k4server")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(cfgDir, "_default.cfg")
	if err := os.WriteFile(defaultPath, []byte("port=\"7777\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	im := NewStaticInstanceManager(t.TempDir(), GameServerInstance{ID: "ut", Dir: dir, Script: "ut2k4server"})
	if _, err := im.SaveConfigFileContent("ut", defaultPath, "port=\"7778\"\n"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(cfgDir, "ut2k4server.cfg")); string(data) != "port=\"7778\"\n" {
		t.Errorf("instance config = %q", data)
	}
}
