package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The Docker mode runs schedules inside the dashboard, so their time zone is the dashboard's.
// Administrators can set it in the settings instead of passing TZ to the container: the
// setting wins, then TZ, then UTC. On a host the system crontab decides, so there is no setting.

// TimeZoneInfo describes the time zone used for schedules.
type TimeZoneInfo struct {
	TimeZone  string `json:"timezone"`  // configured zone, "" = default (TZ or UTC)
	Effective string `json:"effective"` // zone in use
	Default   string `json:"default"`   // zone used without a setting
	Now       string `json:"now"`       // current time in that zone (RFC 3339)
}

// scheduleTimeZoner is implemented by runtimes whose schedules use a configurable time zone.
type scheduleTimeZoner interface {
	TimeZoneInfo() TimeZoneInfo
	SetTimeZone(name string) error
}

var reTimeZone = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

// LoadTimeZone validates an IANA time zone name such as "Europe/Berlin" or "UTC".
func LoadTimeZone(name string) (*time.Location, error) {
	if len(name) > 64 || !reTimeZone.MatchString(name) || strings.EqualFold(name, "Local") {
		return nil, fmt.Errorf("invalid time zone %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	return loc, nil
}

// timeZoneSetting stores the schedule time zone in the dashboard configuration.
type timeZoneSetting struct {
	mu   sync.RWMutex
	path string // <config dir>/schedule_settings.json, "" when not persisted
	name string
	loc  *time.Location
}

type timeZoneFile struct {
	TimeZone string `json:"timezone"`
}

// defaultTimeZoneName is the zone used without a setting: TZ (as Go applies it) or UTC.
func defaultTimeZoneName() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	return "UTC"
}

func newTimeZoneSetting(configDir string) *timeZoneSetting {
	s := &timeZoneSetting{}
	if configDir == "" {
		return s
	}
	s.path = filepath.Join(configDir, "schedule_settings.json")
	data, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var f timeZoneFile
	if json.Unmarshal(data, &f) != nil || f.TimeZone == "" {
		return s
	}
	if loc, err := LoadTimeZone(f.TimeZone); err == nil {
		s.name, s.loc = f.TimeZone, loc
	} else {
		fmt.Printf("[WARNING] Ignoring the schedule time zone in %s: %v\n", s.path, err)
	}
	return s
}

// location returns the zone schedules are evaluated in.
func (s *timeZoneSetting) location() *time.Location {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loc != nil {
		return s.loc
	}
	return time.Local
}

func (s *timeZoneSetting) info() TimeZoneInfo {
	s.mu.RLock()
	name := s.name
	s.mu.RUnlock()
	effective := name
	if effective == "" {
		effective = defaultTimeZoneName()
	}
	return TimeZoneInfo{
		TimeZone:  name,
		Effective: effective,
		Default:   defaultTimeZoneName(),
		Now:       time.Now().In(s.location()).Format(time.RFC3339),
	}
}

// set stores name ("" restores the default).
func (s *timeZoneSetting) set(name string) error {
	name = strings.TrimSpace(name)
	var loc *time.Location
	if name != "" {
		var err error
		if loc, err = LoadTimeZone(name); err != nil {
			return err
		}
	}
	if s.path == "" {
		return fmt.Errorf("the time zone cannot be stored: no configuration directory")
	}
	data, _ := json.MarshalIndent(timeZoneFile{TimeZone: name}, "", "  ")
	if err := WriteFileAtomic(s.path, append(data, '\n'), 0600); err != nil {
		return err
	}
	s.mu.Lock()
	s.name, s.loc = name, loc
	s.mu.Unlock()
	return nil
}

// ScheduleTimeZone returns the schedule time zone; ok is false when the runtime has none
// (on a host the system crontab uses the system time zone).
func (im *InstanceManager) ScheduleTimeZone() (info TimeZoneInfo, ok bool) {
	tz, ok := im.rt.(scheduleTimeZoner)
	if !ok {
		return TimeZoneInfo{}, false
	}
	return tz.TimeZoneInfo(), true
}

// SetScheduleTimeZone changes the schedule time zone ("" = default).
func (im *InstanceManager) SetScheduleTimeZone(name string) error {
	tz, ok := im.rt.(scheduleTimeZoner)
	if !ok {
		return fmt.Errorf("the schedule time zone is set by the operating system in this runtime")
	}
	return tz.SetTimeZone(name)
}
