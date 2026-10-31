package backend

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runtime abstracts where game servers live and how the dashboard controls them. The host
// runtime runs LinuxGSM directly on this machine as Linux users (runuser, crontab, systemd);
// further runtimes (for example Docker containers) implement the same operations.
//
// File access (configs, logs, backups, file manager) is not part of the interface: every runtime
// exposes the server files on a path the dashboard can read, which is GameServerInstance.Dir.
type Runtime interface {
	// Name identifies the runtime ("host").
	Name() string
	// Capabilities lists the optional features this runtime supports.
	Capabilities() Capabilities
	// Init runs once when the instance manager starts.
	Init()

	// Discover returns all game server instances with unique IDs. customPaths are the additional
	// server directories configured by an administrator.
	Discover(customPaths []string) ([]*GameServerInstance, error)
	// ProcessStats returns the CPU usage (percent), RAM usage (GB) and PIDs of a server.
	ProcessStats(srv GameServerInstance) (cpu float64, ram float64, pids []int)

	// RunLGSM runs the LinuxGSM script of srv with args, with the privileges of the server.
	RunLGSM(ctx context.Context, srv GameServerInstance, stdio ExecIO, args ...string) error
	// RunAsUser runs a program (argv, no shell) with the privileges of the server. login selects
	// the environment of a login shell (HOME, PATH and the user's own configuration).
	RunAsUser(ctx context.Context, srv GameServerInstance, stdio ExecIO, login bool, args ...string) error

	// ValidateInstall checks an installation request before the installation starts.
	ValidateInstall(req InstallRequest) error
	// Install sets up a new game server. Progress is reported line by line through logf.
	Install(ctx context.Context, req InstallRequest, logf func(string)) error
	// Clone creates a new server from srv; suffix is validated ([a-z0-9-]). It returns the ID
	// of the new server.
	Clone(ctx context.Context, srv GameServerInstance, suffix string) (string, error)
	// Delete removes a stopped server. sharedUser is set when other instances use the same
	// Linux user, in which case only this instance is removed.
	Delete(ctx context.Context, srv GameServerInstance, sharedUser bool) error

	// ReadCrontab returns the crontab of the server user ("" when there is none).
	ReadCrontab(srv GameServerInstance) (string, error)
	// WriteCrontab replaces the crontab of the server user. Empty content removes it.
	WriteCrontab(srv GameServerInstance, content string) error
	// InstallAutostart makes the server start at boot.
	InstallAutostart(srv GameServerInstance) error
}

// Capabilities tells the API and the interface which optional features a runtime supports.
type Capabilities struct {
	Runtime          string `json:"runtime"`
	LinuxUsers       bool   `json:"linux_users"`        // installer creates a Linux user (username/password fields)
	CustomInstallDir bool   `json:"custom_install_dir"` // installer accepts an installation directory
	ServerPaths      bool   `json:"server_paths"`       // additional server directories are scanned
	MultiInstance    bool   `json:"multi_instance"`     // instances can be cloned next to an existing one
	Cron             bool   `json:"cron"`               // scheduler and backup schedule (crontab)
	Autostart        bool   `json:"autostart"`          // autostart service at boot (systemd)
	Firewall         bool   `json:"firewall"`           // firewall status and opening ports
	SelfUpdate       bool   `json:"self_update"`        // dashboard binary updates itself
	ScheduleTimeZone bool   `json:"schedule_timezone"`  // schedule time zone is set in the dashboard
}

// ExecIO connects a command to the caller. Nil fields mean no input and discarded output.
type ExecIO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// InstallRequest describes a new game server installation.
type InstallRequest struct {
	GameCmd  string // LinuxGSM script name of the game, e.g. vhserver
	Username string // Linux user to create (host runtime)
	Password string // optional password of that user
	Dir      string // optional installation directory, empty for the default
	Lang     string // language of the progress messages
}

// ExitError reports a command that ran but exited with a non-zero code. Runtimes that do not
// use os/exec return it so callers can read the exit code the same way.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// StartError reports a command that could not be started at all.
type StartError struct {
	Err error
}

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// ExitCode returns the exit code of a finished command: 0 without error, the process exit code
// for exit errors and 1 for every other error (command could not be started, I/O errors).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	var rtErr *ExitError
	if errors.As(err, &rtErr) {
		return rtErr.Code
	}
	return 1
}

// isExitError reports whether err means "the command ran and failed" as opposed to "the
// command could not be run".
func isExitError(err error) bool {
	var exitErr *exec.ExitError
	var rtErr *ExitError
	return errors.As(err, &exitErr) || errors.As(err, &rtErr)
}

// isStartError reports whether err means the command could not be started.
func isStartError(err error) bool {
	var startErr *StartError
	return errors.As(err, &startErr)
}

// RuntimeMode decides between the host and the Docker runtime. The LGSM_RUNTIME environment
// variable ("host" or "docker") wins; without it a dashboard running inside a container uses
// Docker and every other installation the host.
func RuntimeMode() (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv("LGSM_RUNTIME"))); mode {
	case "host", "docker":
		return mode, nil
	case "", "auto":
		if InContainer() {
			return "docker", nil
		}
		return "host", nil
	default:
		return "", fmt.Errorf("unsupported runtime %q (LGSM_RUNTIME must be host or docker)", mode)
	}
}

// SelectRuntime creates the runtime chosen by RuntimeMode. The Docker runtime fails when the
// engine is not reachable, so a misconfigured container stops with a clear message. configDir is
// the dashboard configuration directory (Docker mode keeps the schedules there).
func SelectRuntime(configDir string) (Runtime, error) {
	mode, err := RuntimeMode()
	if err != nil {
		return nil, err
	}
	if mode == "docker" {
		rt, err := NewDockerRuntime(os.Getenv("LGSM_DATA_DIR"), configDir)
		if err != nil {
			return nil, err
		}
		return rt, nil
	}
	return HostRuntime{}, nil
}

// -------------------------------------------------------------
// Helpers on top of the runtime
// -------------------------------------------------------------

// runLGSM runs a LinuxGSM command and discards its output.
func (im *InstanceManager) runLGSM(ctx context.Context, srv GameServerInstance, args ...string) error {
	return im.rt.RunLGSM(ctx, srv, ExecIO{}, args...)
}

// outputLGSM runs a LinuxGSM command and returns its standard output.
func (im *InstanceManager) outputLGSM(ctx context.Context, srv GameServerInstance, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := im.rt.RunLGSM(ctx, srv, ExecIO{Stdout: &out}, args...)
	return out.Bytes(), err
}

// streamLines runs fn with a writer that receives stdout and stderr and calls onLine for every
// line (including its line break) while the command runs. readErr is called when reading the
// output failed for another reason than the end of the output.
func streamLines(run func(w io.Writer) error, onLine func(string), readErr func(error)) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := run(pw)
		pw.Close()
		done <- err
	}()

	reader := bufio.NewReader(pr)
	for {
		line, err := readLineOrCR(reader)
		if line != "" {
			onLine(line)
		}
		if err != nil {
			if err != io.EOF && readErr != nil {
				readErr(err)
			}
			break
		}
	}
	// Keep draining so the command never blocks on a full pipe after a read error.
	go io.Copy(io.Discard, pr)
	return <-done
}

// readLineOrCR reads up to and including the next \n or a lone \r. Programs redraw a line with
// \r (LinuxGSM's "[ .... ]" status becoming "[  OK  ]", SteamCMD's progress), so such a line is
// passed on right away instead of when the next \n arrives.
func readLineOrCR(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		c, err := r.ReadByte()
		if err != nil {
			return b.String(), err
		}
		b.WriteByte(c)
		if c == '\n' {
			return b.String(), nil
		}
		if c == '\r' {
			if next, err := r.Peek(1); err == nil && next[0] == '\n' {
				continue // \r\n is a normal line end
			}
			return b.String(), nil
		}
	}
}
