package provider

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/state"
)

// MoshiSnapshot is the wire format emitted by `sslug usage --format moshi`.
// It mirrors moshi-hook's observed usage snapshot schema exactly.
type MoshiSnapshot struct {
	AccountID    string        `json:"accountId"`
	AccountLabel string        `json:"accountLabel"`
	Agent        string        `json:"agent"`
	HostName     string        `json:"hostName"`
	CapturedAt   time.Time     `json:"capturedAt"`
	Windows      []MoshiWindow `json:"windows"`
	Cost         *MoshiCost    `json:"cost,omitempty"`
}

// MoshiWindow is one usage window (meter cycle).
type MoshiWindow struct {
	Label          string    `json:"label"`
	UsedPercentage float64   `json:"usedPercentage"`
	ResetsAt       time.Time `json:"resetsAt"`
}

// MoshiCost carries dollar-denominated meter values.
type MoshiCost struct {
	Used  float64 `json:"used"`
	Limit float64 `json:"limit"`
	Unit  string  `json:"unit"`
}

// MoshiBuild converts config + state into one MoshiSnapshot per provider
// that has meters with a resolvable value.
// now is injected for deterministic tests.
func MoshiBuild(cfg config.Config, st *state.File, now time.Time) []MoshiSnapshot {
	hostname, _ := os.Hostname()
	var out []MoshiSnapshot
	for _, p := range cfg.Providers {
		// Note: no Enabled filter — disabled providers still carry meters.
		snap := MoshiSnapshot{
			AccountID:    "sslug:" + p.Name,
			AccountLabel: p.Name,
			Agent:        "sslug",
			HostName:     hostname,
			CapturedAt:   now,
		}
		for _, m := range p.Meters {
			mv := st.GetMeter(p.Name, m.Name)
			// Fall back to the config-defined initial value — the same
			// fallback BuildSnapshot and the usage pane apply, so all three
			// JSON/TUI surfaces agree about the same meter.
			value := m.Used
			if mv != nil {
				value = mv.Value
			} else if m.Used == 0 && m.Cap == 0 {
				continue // nothing configured at all — skip
			}
			resetsAt := resolveReset(m.Reset, now)
			var pct float64
			if m.Cap > 0 {
				pct = value / m.Cap * 100
			}
			snap.Windows = append(snap.Windows, MoshiWindow{
				Label:          m.Name,
				UsedPercentage: pct,
				ResetsAt:       resetsAt,
			})
			if m.Unit == "USD" && snap.Cost == nil {
				snap.Cost = &MoshiCost{Used: value, Limit: m.Cap, Unit: "USD"}
			}
		}
		if len(snap.Windows) > 0 || snap.Cost != nil {
			out = append(out, snap)
		}
	}
	return out
}

// resolveReset converts a reset spec to the next reset time from now.
func resolveReset(spec string, now time.Time) time.Time {
	if spec == "" || spec == "never" {
		return now
	}
	return nextReset(spec, now)
}

// nextReset computes the next occurrence of the reset spec after now.
// Specs: "monthly:N" (day 1-31), "weekly:mon..sun", "date:YYYY-MM-DD", "never".
func nextReset(spec string, now time.Time) time.Time {
	kind, arg, _ := strings.Cut(spec, ":")
	switch kind {
	case "monthly":
		var day int
		fmt.Sscanf(arg, "%d", &day)
		if day < 1 {
			day = 1
		}
		if day > 31 {
			day = 31
		}
		// Try this month, then next month.
		y, m, _ := now.Date()
		loc := now.Location()
		candidate := clampedDate(y, m, day, loc)
		if !candidate.After(now) {
			candidate = clampedDate(y, m+1, day, loc)
		}
		return candidate
	case "weekly":
		target := parseWeekday(arg)
		loc := now.Location()
		d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		for i := 0; i < 8; i++ {
			if d.Weekday() == target && d.After(now) {
				return d
			}
			d = d.AddDate(0, 0, 1)
		}
		return now
	case "date":
		t, err := time.ParseInLocation("2006-01-02", arg, now.Location())
		if err != nil {
			return now
		}
		return t
	default:
		return now
	}
}

// NextResetForTest exposes nextReset for unit tests.
var NextResetForTest = nextReset

// clampedDate returns the last valid day of month y/m if day exceeds it.
func clampedDate(y int, m time.Month, day int, loc *time.Location) time.Time {
	if m > 12 {
		m -= 12
		y++
	}
	t := time.Date(y, m, day, 0, 0, 0, 0, loc)
	// If day overflowed into next month, step back to last day of m.
	if t.Month() != m {
		t = time.Date(y, m+1, 0, 0, 0, 0, 0, loc)
	}
	return t
}

func parseWeekday(s string) time.Weekday {
	switch strings.ToLower(s) {
	case "sun":
		return time.Sunday
	case "mon":
		return time.Monday
	case "tue":
		return time.Tuesday
	case "wed":
		return time.Wednesday
	case "thu":
		return time.Thursday
	case "fri":
		return time.Friday
	case "sat":
		return time.Saturday
	}
	return time.Sunday
}
