package backend

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSpec is a parsed five-field cron schedule (minute hour day-of-month month day-of-week)
// with the semantics of Vixie cron: when both day fields are restricted, a day matches if
// either of them matches.
type CronSpec struct {
	minute, hour, dom, month, dow uint64 // bit sets
	domStar, dowStar              bool
}

var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// ParseCron parses a cron expression as accepted by IsValidCronExpr: five fields with *, numbers,
// ranges, lists and steps, or one of the @ macros.
func ParseCron(expr string) (CronSpec, error) {
	expr = strings.TrimSpace(expr)
	if m, ok := cronMacros[expr]; ok {
		expr = m
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return CronSpec{}, fmt.Errorf("cron expression needs 5 fields")
	}
	var spec CronSpec
	var err error
	if spec.minute, err = parseCronField(fields[0], 0, 59); err != nil {
		return CronSpec{}, fmt.Errorf("minute: %w", err)
	}
	if spec.hour, err = parseCronField(fields[1], 0, 23); err != nil {
		return CronSpec{}, fmt.Errorf("hour: %w", err)
	}
	if spec.dom, err = parseCronField(fields[2], 1, 31); err != nil {
		return CronSpec{}, fmt.Errorf("day of month: %w", err)
	}
	if spec.month, err = parseCronField(fields[3], 1, 12); err != nil {
		return CronSpec{}, fmt.Errorf("month: %w", err)
	}
	if spec.dow, err = parseCronField(fields[4], 0, 7); err != nil {
		return CronSpec{}, fmt.Errorf("day of week: %w", err)
	}
	if spec.dow&(1<<7) != 0 { // 7 is Sunday as well
		spec.dow |= 1
	}
	spec.domStar = strings.HasPrefix(fields[2], "*")
	spec.dowStar = strings.HasPrefix(fields[4], "*")
	return spec, nil
}

func parseCronField(field string, min, max int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		rangePart, step := part, 1
		if i := strings.Index(part, "/"); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid step in %q", part)
			}
			rangePart, step = part[:i], n
		}
		lo, hi := min, max
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			bounds := strings.SplitN(rangePart, "-", 2)
			a, err1 := strconv.Atoi(bounds[0])
			b, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil || a > b {
				return 0, fmt.Errorf("invalid range %q", rangePart)
			}
			lo, hi = a, b
		default:
			n, err := strconv.Atoi(rangePart)
			if err != nil {
				return 0, fmt.Errorf("invalid value %q", rangePart)
			}
			lo, hi = n, n
			if step > 1 { // "5/15" means 5-max/15
				hi = max
			}
		}
		if lo < min || hi > max {
			return 0, fmt.Errorf("value out of range in %q", part)
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

// Matches reports whether the schedule is due in the minute of t.
func (s CronSpec) Matches(t time.Time) bool {
	if s.minute&(1<<uint(t.Minute())) == 0 || s.hour&(1<<uint(t.Hour())) == 0 || s.month&(1<<uint(t.Month())) == 0 {
		return false
	}
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return domOK && dowOK
	}
	return domOK || dowOK
}
