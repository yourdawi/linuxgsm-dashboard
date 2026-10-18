package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// Schedules use the local time of TZ. The container image has no zoneinfo files, so the
	// time zone database is built in.
	_ "time/tzdata"
)

// In Docker mode the dashboard is the scheduler: the crontab inside a container is overwritten on
// every container start, and the game server containers should not be able to plan commands for
// themselves. Each server gets a crontab-like file in the dashboard configuration
// (<config dir>/schedules/<server>.cron), written by the same code as the host crontab. Once a
// minute the scheduler runs the due LinuxGSM commands through docker exec.

// scheduledCommands are the LinuxGSM commands a schedule may run.
var scheduledCommands = map[string]bool{"monitor": true, "update": true, "force-update": true, "update-lgsm": true, "backup": true}

// scheduleTimeout bounds a single scheduled command (updates and backups of large servers).
const scheduleTimeout = 6 * time.Hour

func (d *DockerRuntime) scheduleFile(srv GameServerInstance) (string, error) {
	if d.scheduleDir == "" {
		return "", fmt.Errorf("schedules are not available: no configuration directory")
	}
	if !IsValidServerID(srv.ID) {
		return "", fmt.Errorf("invalid server id")
	}
	return filepath.Join(d.scheduleDir, srv.ID+".cron"), nil
}

// ReadCrontab returns the schedule of a server ("" when there is none).
func (d *DockerRuntime) ReadCrontab(srv GameServerInstance) (string, error) {
	path, err := d.scheduleFile(srv)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// WriteCrontab replaces the schedule of a server; empty content removes it.
func (d *DockerRuntime) WriteCrontab(srv GameServerInstance, content string) error {
	path, err := d.scheduleFile(srv)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(d.scheduleDir, 0700); err != nil {
		return err
	}
	return WriteFileAtomic(path, []byte(trimmed+"\n"), 0600)
}

// scheduledJob is one due command of one server.
type scheduledJob struct {
	serverID string
	command  string
}

// dueJobs returns the commands whose schedule matches the minute of t. Lines are only accepted
// in the form the dashboard writes them: "<schedule> /app/<script> <command> ...", for the script
// the schedule file belongs to (checked against the container label when the job runs).
func dueJobs(scheduleDir string, t time.Time) []scheduledJob {
	files, err := filepath.Glob(filepath.Join(scheduleDir, "*.cron"))
	if err != nil {
		return nil
	}
	var jobs []scheduledJob
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".cron")
		if !IsValidServerID(id) {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
				continue
			}
			idx := -1
			for i, f := range fields {
				if strings.HasPrefix(f, dockerScriptDir+"/") {
					idx = i
					break
				}
			}
			if idx < 1 || idx+1 >= len(fields) || !IsValidCronExpr(strings.Join(fields[:idx], " ")) {
				continue
			}
			command := fields[idx+1]
			if !scheduledCommands[command] || seen[command] {
				continue
			}
			spec, err := ParseCron(strings.Join(fields[:idx], " "))
			if err != nil || !spec.Matches(t) {
				continue
			}
			seen[command] = true
			jobs = append(jobs, scheduledJob{serverID: id, command: command})
		}
	}
	return jobs
}

// startScheduler runs the scheduler loop in the background.
func (d *DockerRuntime) startScheduler() {
	if d.scheduleDir == "" {
		return
	}
	go func() {
		defer logPanic("scheduler")
		for {
			next := time.Now().Truncate(time.Minute).Add(time.Minute)
			time.Sleep(time.Until(next))
			d.runDue(next)
		}
	}()
}

// runDue starts the jobs due at t. The commands due for one server run one after the other (two
// LinuxGSM commands at once would collide on its lock files). When the commands of an earlier
// minute are still running for that server, the new ones are skipped.
func (d *DockerRuntime) runDue(t time.Time) {
	jobs := dueJobs(d.scheduleDir, t.In(d.scheduleLocation()))
	if len(jobs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	containers, err := d.client.ListContainers(ctx, dockerLabelManaged+"=true")
	cancel()
	if err != nil {
		fmt.Printf("[SCHEDULE] cannot list containers: %v\n", err)
		return
	}
	servers := map[string]*GameServerInstance{}
	for _, inst := range dockerInstances(containers, d.dataDir) {
		servers[inst.ID] = inst
	}
	perServer := map[string][]string{}
	var order []string
	for _, job := range jobs {
		if _, ok := servers[job.serverID]; !ok {
			continue
		}
		if perServer[job.serverID] == nil {
			order = append(order, job.serverID)
		}
		perServer[job.serverID] = append(perServer[job.serverID], job.command)
	}
	for _, id := range order {
		go d.runJobs(*servers[id], perServer[id])
	}
}

// scheduleLocation is the time zone schedules are evaluated in.
func (d *DockerRuntime) scheduleLocation() *time.Location {
	if d.timeZone == nil {
		return time.Local
	}
	return d.timeZone.location()
}

// TimeZoneInfo reports the schedule time zone.
func (d *DockerRuntime) TimeZoneInfo() TimeZoneInfo {
	if d.timeZone == nil {
		return newTimeZoneSetting("").info()
	}
	return d.timeZone.info()
}

// SetTimeZone changes the schedule time zone; "" restores the default (TZ or UTC).
func (d *DockerRuntime) SetTimeZone(name string) error {
	if d.timeZone == nil {
		return fmt.Errorf("the time zone cannot be stored: no configuration directory")
	}
	return d.timeZone.set(name)
}

var scheduleBusy sync.Map // server ID -> struct{}

func (d *DockerRuntime) runJobs(srv GameServerInstance, commands []string) {
	defer logPanic("scheduled commands of " + srv.ID)
	if _, busy := scheduleBusy.LoadOrStore(srv.ID, struct{}{}); busy {
		fmt.Printf("[SCHEDULE] %s: %s skipped, earlier scheduled commands are still running\n", srv.ID, strings.Join(commands, ", "))
		return
	}
	defer scheduleBusy.Delete(srv.ID)

	for _, command := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), scheduleTimeout)
		start := time.Now()
		err := d.RunLGSM(ctx, srv, ExecIO{}, command)
		cancel()
		fmt.Printf("[SCHEDULE] %s: %s finished with exit code %d after %s\n", srv.ID, command, ExitCode(err), time.Since(start).Round(time.Second))
	}
}
