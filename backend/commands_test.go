package backend

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLinuxGSMCommands(t *testing.T) {
	has := func(cmds []string, want ...string) bool {
		for _, w := range want {
			if !slices.Contains(cmds, w) {
				return false
			}
		}
		return true
	}
	cs2 := linuxGSMCommands(map[string]string{"engine": "source2", "appid": "730", "consoleinteract": "yes"}, "cs2")
	if !has(cs2, "update", "force-update", "validate", "send", "mods-install") || slices.Contains(cs2, "fastdl") {
		t.Errorf("cs2: %v", cs2)
	}
	css := linuxGSMCommands(map[string]string{"engine": "source", "appid": "232330"}, "css")
	if !has(css, "fastdl", "mods-install") || slices.Contains(css, "send") {
		t.Errorf("css: %v", css)
	}
	ts3 := linuxGSMCommands(map[string]string{"consoleinteract": "no"}, "ts3")
	if !has(ts3, "update", "change-password") || has(ts3, "force-update") || has(ts3, "validate") || has(ts3, "send") {
		t.Errorf("ts3: %v", ts3)
	}
	q3 := linuxGSMCommands(map[string]string{"engine": "idtech3"}, "q3")
	if has(q3, "update") || !has(q3, "backup", "start") {
		t.Errorf("q3: %v", q3)
	}
	rust := linuxGSMCommands(map[string]string{"appid": "258550"}, "rust")
	if !has(rust, "map-wipe", "full-wipe", "mods-install") || has(rust, "map-compressor") {
		t.Errorf("rust: %v", rust)
	}
	if ut := linuxGSMCommands(map[string]string{"engine": "unreal2"}, "ut2k4"); !has(ut, "map-compressor") || has(ut, "update") {
		t.Errorf("ut2k4: %v", ut)
	}
	if a3 := linuxGSMCommands(map[string]string{"appid": "233780"}, "arma3"); !has(a3, "workshop-update") || has(a3, "mods-install") {
		t.Errorf("arma3: %v", a3)
	}
	for _, c := range linuxGSMCommands(map[string]string{"engine": "source", "appid": "1"}, "rust") {
		if _, ok := ActionPermission(c); !ok && c != "send" && c != "monitor" {
			t.Errorf("%s has no permission", c)
		}
	}
}

func TestDirLocks(t *testing.T) {
	var l dirLocks
	release, ok := l.tryLock("/home/vh")
	if !ok {
		t.Fatal("lock refused")
	}
	if _, ok := l.tryLock("/home/vh"); ok {
		t.Fatal("second lock on the same directory")
	}
	if r, ok := l.tryLock("/home/other"); !ok {
		t.Fatal("other directory blocked")
	} else {
		r()
	}
	got := make(chan struct{})
	go func() {
		l.lock("/home/vh")()
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("waiting lock did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting lock never got it")
	}
}

func TestCheckActionUsesCommands(t *testing.T) {
	srv := GameServerInstance{ID: "ts3server", Script: "ts3server", Dir: t.TempDir(), Commands: []string{"start", "stop", "update"}}
	im := NewStaticInstanceManager(t.TempDir(), srv)
	if err := im.checkAction(srv, "update"); err != nil {
		t.Fatal(err)
	}
	if err := im.checkAction(srv, "validate"); err == nil {
		t.Error("validate allowed for a game without it")
	}
	old := GameServerInstance{ID: "x", Dir: t.TempDir()} // unknown commands: everything allowed
	if err := im.checkAction(old, "validate"); err != nil {
		t.Error(err)
	}
	if err := im.SendConsoleCommand("ts3server", "say hi"); err == nil {
		t.Error("console input accepted for a game without send")
	}
}

func TestNewestGameLog(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "lgsm", "config-lgsm", "vhserver", "_default.cfg"), "systemdir=\"${serverfiles}\"\ngamelogdir=\"${systemdir}/logs\"\n")
	logs := filepath.Join(dir, "serverfiles", "logs")
	mustWrite(t, filepath.Join(logs, "a-old.log"), "old\n")
	mustWrite(t, filepath.Join(logs, "z-new.log"), "new\n")
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(logs, "a-old.log"), past, past)
	root, rel, ok := newestGameLog(GameServerInstance{Script: "vhserver", Dir: dir})
	if !ok || rel != "z-new.log" || filepath.Base(root) != "logs" {
		t.Fatalf("got %s %s %v", root, rel, ok)
	}
}
