package backend

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// LinuxGSM installs missing packages with sudo during auto-install. Giving the game server user
// sudo would make it root for the whole installation (downloaded game code included), so the
// dashboard installs the packages itself, as root, before auto-install runs as the user. The
// package list is LinuxGSM's own lgsm/data/<distro>-<version>.csv, fetched from GitHub like
// LinuxGSM does, never from the user's copy: rows "all", "steamcmd" and the game's shortname.
// Packages LinuxGSM adds from other repositories (Mono, .NET, Java on old releases) are not
// covered; LinuxGSM reports them as missing.

// lgsmRawURL is the LinuxGSM repository the files are read from; tests override it.
var lgsmRawURL = "https://raw.githubusercontent.com/GameServerManagers/LinuxGSM/master/"

var rePackageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,100}(:i386)?$`)

// osRelease reads ID and VERSION_ID from /etc/os-release.
func osRelease(path string) (id, version string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			version = v
		}
	}
	return id, version
}

// depsCSVName returns LinuxGSM's dependency file for a distribution ("" when unsupported).
func depsCSVName(id, version string) string {
	switch id {
	case "ubuntu", "debian":
	case "rhel", "almalinux", "rocky", "centos":
		version, _, _ = strings.Cut(version, ".")
	default:
		return ""
	}
	if !regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`).MatchString(version) {
		return ""
	}
	return id + "-" + version + ".csv"
}

// parseDepsCSV returns the packages of the rows "all" and shortname, and of the row "steamcmd".
func parseDepsCSV(data, shortname string) (base, steamcmd []string) {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 {
			continue
		}
		var target *[]string
		switch fields[0] {
		case "all", shortname:
			target = &base
		case "steamcmd":
			target = &steamcmd
		default:
			continue
		}
		for _, p := range fields[1:] {
			p = strings.TrimSpace(p)
			if rePackageName.MatchString(p) {
				*target = append(*target, p)
			}
		}
	}
	return base, steamcmd
}

// shortnameOf maps a LinuxGSM script name (cs2server) to its shortname (cs2).
func shortnameOf(gameCmd string) string {
	return strings.TrimSuffix(gameCmd, "server")
}

// fetchLGSMFile reads a file of the LinuxGSM repository, e.g. lgsm/data/ubuntu-24.04.csv.
func fetchLGSMFile(ctx context.Context, rel string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lgsmRawURL+rel, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", rel, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(data), err
}

// depsRunner runs a package manager command as root; tests replace it.
var depsRunner = func(ctx context.Context, stdin string, logf func(string), name string, args ...string) error {
	name, args = outsideServiceSandbox(name, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return streamLines(func(w io.Writer) error {
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Start(); err != nil {
			return &StartError{Err: err}
		}
		return cmd.Wait()
	}, func(line string) { logf(stripAnsi(line)) }, nil)
}

// packageInstalled reports whether a dpkg or rpm package is installed; tests replace it.
var packageInstalled = func(manager, pkg string) bool {
	if manager == "apt" {
		out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", pkg).Output()
		return err == nil && strings.Contains(string(out), "install ok installed")
	}
	return exec.Command("rpm", "-q", "--whatprovides", pkg).Run() == nil
}

// installDependencies installs what LinuxGSM needs for gameCmd, the SteamCMD packages only
// withSteamCMD. Problems are reported through logf and never fail the installation: LinuxGSM
// checks again and names what is missing.
func installDependencies(ctx context.Context, gameCmd string, withSteamCMD bool, lang string, logf func(string)) {
	logLine := func(s string) { logf(s + "\n") }
	id, version := osRelease("/etc/os-release")
	name := depsCSVName(id, version)
	if name == "" {
		logLine(Msg(lang, "[INSTALL] No LinuxGSM dependency list for this distribution, skipping the package check.",
			"[INSTALL] Keine LinuxGSM-Abhängigkeitsliste für diese Distribution, überspringe die Paketprüfung."))
		return
	}
	data, err := fetchLGSMFile(ctx, "lgsm/data/"+name)
	if err != nil {
		logLine(Msg(lang, "[WARNING] Could not load the dependency list: ", "[WARNUNG] Abhängigkeitsliste konnte nicht geladen werden: ") + err.Error())
		return
	}
	base, steam := parseDepsCSV(data, shortnameOf(gameCmd))
	if !withSteamCMD {
		steam = nil // like LinuxGSM: only games with a Steam app ID use SteamCMD
	}

	manager := "apt"
	if id != "ubuntu" && id != "debian" {
		manager = "dnf"
		if _, err := exec.LookPath("dnf"); err != nil {
			manager = "yum"
		}
	}
	missing := func(pkgs []string) []string {
		var out []string
		seen := map[string]bool{}
		for _, p := range pkgs {
			if !seen[p] && !packageInstalled(manager, p) {
				out = append(out, p)
			}
			seen[p] = true
		}
		return out
	}
	baseMissing, steamMissing := missing(base), missing(steam)
	if len(baseMissing) == 0 && len(steamMissing) == 0 {
		logLine(Msg(lang, "[INSTALL] All dependencies are installed.", "[INSTALL] Alle Abhängigkeiten sind installiert."))
		return
	}
	logLine(Msg(lang, "[INSTALL] Installing missing dependencies: ", "[INSTALL] Installiere fehlende Abhängigkeiten: ") +
		strings.Join(append(append([]string{}, baseMissing...), steamMissing...), " "))

	warn := func(err error) {
		logLine(Msg(lang, "[WARNING] Installing packages failed: ", "[WARNUNG] Paketinstallation fehlgeschlagen: ") + err.Error())
	}
	if manager != "apt" {
		if len(baseMissing) > 0 {
			if err := depsRunner(ctx, "", logf, manager, append([]string{"-y", "install"}, baseMissing...)...); err != nil {
				warn(err)
			}
		}
		// SteamCMD comes from a separate repository; LinuxGSM falls back to its own download.
		if len(steamMissing) > 0 {
			if err := depsRunner(ctx, "", logf, manager, append([]string{"-y", "install"}, steamMissing...)...); err != nil {
				warn(err)
			}
		}
		return
	}

	for _, p := range append(append([]string{}, baseMissing...), steamMissing...) {
		if strings.HasSuffix(p, ":i386") {
			if err := depsRunner(ctx, "", logf, "dpkg", "--add-architecture", "i386"); err != nil {
				warn(err)
			}
			break
		}
	}
	_ = depsRunner(ctx, "steamcmd steam/question select I AGREE\nsteamcmd steam/license note ''\n", logf, "debconf-set-selections")
	if err := depsRunner(ctx, "", logf, "apt-get", "update"); err != nil {
		warn(err)
	}
	if len(baseMissing) > 0 {
		if err := depsRunner(ctx, "", logf, "apt-get", append([]string{"-y", "install", "--"}, baseMissing...)...); err != nil {
			warn(err)
		}
	}
	// steamcmd needs multiverse (Ubuntu) or non-free (Debian); without it LinuxGSM downloads
	// SteamCMD itself, so a failure here is fine.
	if len(steamMissing) > 0 {
		if err := depsRunner(ctx, "", logf, "apt-get", append([]string{"-y", "install", "--"}, steamMissing...)...); err != nil {
			warn(err)
		}
	}
}

var reAppID = regexp.MustCompile(`^\s*appid="?([0-9]+)"?`)

// usesSteamCMD reports whether the game's default config in the LinuxGSM repository has a
// Steam app ID (the bootstrap does not download it yet).
func usesSteamCMD(ctx context.Context, gameCmd string) bool {
	data, err := fetchLGSMFile(ctx, "lgsm/config-default/config-lgsm/"+gameCmd+"/_default.cfg")
	if err != nil {
		return true // unknown: install it, LinuxGSM decides later
	}
	for _, line := range strings.Split(data, "\n") {
		if reAppID.MatchString(line) {
			return true
		}
	}
	return false
}
