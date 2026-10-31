package backend

import (
	"slices"
	"strings"
)

// linuxGSMCommands returns the commands LinuxGSM offers for a game, following
// lgsm/modules/core_getopt.sh: values are the merged LinuxGSM configs (engine, appid,
// consoleinteract), shortname is the game's short name (vh for vhserver). Only commands the
// dashboard can run are listed; "send" stands for console input.
func linuxGSMCommands(values map[string]string, shortname string) []string {
	engine := values["engine"]
	appid := values["appid"]
	is := func(names ...string) bool { return slices.Contains(names, shortname) }

	cmds := []string{"start", "stop", "restart", "monitor", "test-alert", "details", "postdetails", "skeleton", "update-lgsm"}
	if is("jk2", "jk3", "etl") || engine != "idtech3" {
		if !is("bf1942", "bfv", "samp", "ut2k4") && engine != "idtech2" && engine != "iw2.0" && engine != "iw3.0" && engine != "quake" {
			cmds = append(cmds, "update", "check-update")
			if appid != "" || is("mta", "hyt") {
				cmds = append(cmds, "force-update")
			}
		}
	}
	if appid != "" {
		cmds = append(cmds, "validate")
	}
	cmds = append(cmds, "backup")
	if values["consoleinteract"] == "yes" {
		cmds = append(cmds, "send")
	}
	if engine == "source" || engine == "goldsrc" {
		cmds = append(cmds, "fastdl")
	}
	if is("ts3") {
		cmds = append(cmds, "change-password")
	}
	if is("rust") {
		cmds = append(cmds, "full-wipe", "map-wipe")
	}
	if engine == "unreal" || engine == "unreal2" {
		cmds = append(cmds, "map-compressor")
	}
	if is("mta") {
		cmds = append(cmds, "install-default-resources")
	}
	if engine == "source" || is("cs2", "rust", "hq", "sdtd", "cs", "dod", "tfc", "ns", "ts", "hldm", "vh") {
		cmds = append(cmds, "mods-install", "mods-remove", "mods-update")
	}
	if is("arma3", "dayz", "bt", "col", "hw", "l4d2", "ohd", "sb", "squad", "squad44", "sol", "st") {
		cmds = append(cmds, "workshop-update")
	}
	return cmds
}

// shortnameOfScript returns the LinuxGSM short name of a script (vh for vhserver-2).
func shortnameOfScript(dir, script string) string {
	return strings.TrimSuffix(gameServerNameOf(dir, script), "server")
}

// HasCommand reports whether LinuxGSM offers command for srv. Unknown command lists allow all.
func (srv GameServerInstance) HasCommand(command string) bool {
	return srv.Commands == nil || slices.Contains(srv.Commands, command)
}
