package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSteamLoginValid(t *testing.T) {
	for _, s := range []SteamLogin{
		{},
		{User: "gamer_1", Pass: `pa"ss$w'o rd\`},
		{User: "me@example.com", Pass: "x"},
		{User: "nopass"},
	} {
		if !s.Valid() {
			t.Errorf("%+v refused", s)
		}
	}
	for _, s := range []SteamLogin{
		{Pass: "without user"},
		{User: "a b", Pass: "x"},
		{User: "a\"b", Pass: "x"},
		{User: "a", Pass: "line\nbreak"},
		{User: "a", Pass: strings.Repeat("x", 257)},
	} {
		if s.Valid() {
			t.Errorf("%+v accepted", s)
		}
	}
}

func TestSeedSteamLogin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the Linux file helpers")
	}
	cfgDir := filepath.Join(t.TempDir(), "lgsm", "config-lgsm", "arma3server")
	file := filepath.Join(cfgDir, "secrets-common.cfg")

	if err := seedSteamLogin(cfgDir, SteamLogin{User: "gamer", Pass: `a"b$c`}, 0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`steamuser="gamer"`, `steampass="a\"b\$c"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in:\n%s", want, data)
		}
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0600 {
		t.Errorf("mode = %o", st.Mode().Perm())
	}

	// A file of an earlier installation keeps its other settings.
	mustWrite(t, file, "discordwebhook=\"https://x\"\nsteamuser=\"old\"\n")
	if err := seedSteamLogin(cfgDir, SteamLogin{User: "new", Pass: "p"}, 0, 0); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(file)
	for _, want := range []string{`discordwebhook="https://x"`, `steamuser="new"`, `steampass="p"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "old") {
		t.Errorf("old login kept:\n%s", data)
	}
}

func TestGamesListMarksSteamLoginGames(t *testing.T) {
	gc := &GameCrawler{games: []GameCrawlItem{{ID: "arma3", Cmd: "arma3server"}, {ID: "vh", Cmd: "vhserver"}}}
	games := gc.GetGames()
	if !games[0].SteamLogin || games[1].SteamLogin {
		t.Fatalf("games = %+v", games)
	}
	for _, g := range (&GameCrawler{}).GetGames() {
		if g.SteamLogin != NeedsSteamLogin(g.Cmd) {
			t.Errorf("fallback list: %+v", g)
		}
	}
}

func TestLGSMDownloadFailed(t *testing.T) {
	if !lgsmDownloadFailed("\x1b[31mFailure!\x1b[0m Installing terrariaserver: SteamCMD: Did not complete the download, too many retrys\n") {
		t.Error("failure not recognised")
	}
	if lgsmDownloadFailed("Error! Installing terrariaserver: SteamCMD: Exit code: 5\n") {
		t.Error("a retry is not the end")
	}
}
