// Package state manages state.json: check results, latency rings,
// usage snapshots, and per-target counters.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// File is the root of state.json.
type File struct {
	UI        UIState                   `json:"ui"`
	Providers map[string]*ProviderState `json:"providers"`
	Meters    map[string]*MeterValue    `json:"meters"` // "<provider>/<meter>" → live value
}

// UIState persists the active view and per-panel prefs.
type UIState struct {
	View   string            `json:"view,omitempty"`
	Panels map[string]string `json:"panels,omitempty"` // panel name → prefs blob
}

// ProviderState holds everything known about one provider.
type ProviderState struct {
	LastCheck    *CheckResult           `json:"last_check,omitempty"`
	Ring         []float64              `json:"ring,omitempty"` // latency ms, oldest first, capped
	Counters     Counters               `json:"counters"`
	RecentErrors []ErrorEntry           `json:"recent_errors,omitempty"` // last 5 non-ok, newest first
	Models       map[string]*ModelState `json:"models,omitempty"`        // model id → state
}

// ModelState holds per-model (favourite) state.
type ModelState struct {
	LastCheck    *CheckResult `json:"last_check,omitempty"`
	Ring         []float64    `json:"ring,omitempty"`
	Counters     Counters     `json:"counters"`
	RecentErrors []ErrorEntry `json:"recent_errors,omitempty"`
}

// ErrorEntry is one non-ok check outcome, kept for the inspect overlay.
type ErrorEntry struct {
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	HTTPCode  int       `json:"http_code,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// CheckResult is the outcome of one probe.
type CheckResult struct {
	Status    string    `json:"status"`           // ok|account|down|unknown
	Reason    string    `json:"reason,omitempty"` // human-readable
	HTTPCode  int       `json:"http_code,omitempty"`
	LatencyMs float64   `json:"latency_ms,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Counters are cumulative per-target tallies.
type Counters struct {
	Checks  int `json:"checks"`
	OK      int `json:"ok"`
	Account int `json:"account"`
	Down    int `json:"down"`
}

// MeterValue is a live manual/auto meter value.
type MeterValue struct {
	Value float64   `json:"value"`
	SetAt time.Time `json:"set_at"`
}

// New returns an empty File.
func New() *File {
	return &File{
		Providers: make(map[string]*ProviderState),
		Meters:    make(map[string]*MeterValue),
	}
}

// Path returns $SSLUG_STATE_HOME/state.json or the XDG default.
func Path() string {
	if d := os.Getenv("SSLUG_STATE_HOME"); d != "" {
		return filepath.Join(d, "state.json")
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "status-slug", "state.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "status-slug", "state.json")
}

// Load reads state.json. On corrupt JSON it writes a .bak and returns fresh state.
func Load() (*File, error) { return LoadFrom(Path()) }

// LoadFrom parses the given file.
func LoadFrom(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	f := New()
	if err := json.Unmarshal(data, f); err != nil {
		bak := path + ".bak"
		_ = os.WriteFile(bak, data, 0o600)
		fmt.Fprintf(os.Stderr, "sslug: corrupt state.json (backed up to %s): %v\n", bak, err)
		return New(), nil
	}
	if f.Providers == nil {
		f.Providers = make(map[string]*ProviderState)
	}
	if f.Meters == nil {
		f.Meters = make(map[string]*MeterValue)
	}
	return f, nil
}

// Save writes state atomically, mode 0600.
func (f *File) Save() error { return f.SaveTo(Path()) }

// SaveTo writes state to path atomically.
func (f *File) SaveTo(path string) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Provider returns (creating if needed) the state for a provider.
func (f *File) Provider(name string) *ProviderState {
	if f.Providers[name] == nil {
		f.Providers[name] = &ProviderState{Models: make(map[string]*ModelState)}
	}
	if f.Providers[name].Models == nil {
		f.Providers[name].Models = make(map[string]*ModelState)
	}
	return f.Providers[name]
}

// Model returns (creating if needed) the state for a provider model.
func (f *File) Model(provider, modelID string) *ModelState {
	p := f.Provider(provider)
	if p.Models[modelID] == nil {
		p.Models[modelID] = &ModelState{}
	}
	return p.Models[modelID]
}

// RecordCheck updates provider state after a probe.
func (f *File) RecordCheck(provider string, res CheckResult, ringCap int) {
	p := f.Provider(provider)
	p.LastCheck = &res
	if res.LatencyMs > 0 {
		p.Ring = appendRing(p.Ring, res.LatencyMs, ringCap)
	}
	p.Counters.Checks++
	switch res.Status {
	case "ok":
		p.Counters.OK++
	case "account":
		p.Counters.Account++
	case "down":
		p.Counters.Down++
	}
	if res.Status != "ok" && res.Status != "unknown" && res.Status != "" {
		p.RecentErrors = appendError(p.RecentErrors, ErrorEntry{
			Status: res.Status, Reason: res.Reason,
			HTTPCode: res.HTTPCode, CheckedAt: res.CheckedAt,
		})
	}
}

// RecordModelCheck updates per-model state after a probe.
func (f *File) RecordModelCheck(provider, modelID string, res CheckResult, ringCap int) {
	m := f.Model(provider, modelID)
	m.LastCheck = &res
	if res.LatencyMs > 0 {
		m.Ring = appendRing(m.Ring, res.LatencyMs, ringCap)
	}
	m.Counters.Checks++
	switch res.Status {
	case "ok":
		m.Counters.OK++
	case "account":
		m.Counters.Account++
	case "down":
		m.Counters.Down++
	}
	if res.Status != "ok" && res.Status != "unknown" && res.Status != "" {
		m.RecentErrors = appendError(m.RecentErrors, ErrorEntry{
			Status: res.Status, Reason: res.Reason,
			HTTPCode: res.HTTPCode, CheckedAt: res.CheckedAt,
		})
	}
}

// appendError prepends an error entry, keeping at most 5 (newest first).
func appendError(errs []ErrorEntry, e ErrorEntry) []ErrorEntry {
	errs = append([]ErrorEntry{e}, errs...)
	if len(errs) > 5 {
		errs = errs[:5]
	}
	return errs
}

// SetMeter records a live meter value.
func (f *File) SetMeter(provider, meter string, value float64) {
	key := provider + "/" + meter
	f.Meters[key] = &MeterValue{Value: value, SetAt: time.Now()}
}

// GetMeter returns the live value for a meter, or nil.
func (f *File) GetMeter(provider, meter string) *MeterValue {
	return f.Meters[provider+"/"+meter]
}

// RelAge formats a duration as a short human string ("2m ago").
func RelAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// appendRing appends v to ring, keeping at most cap entries.
func appendRing(ring []float64, v float64, cap int) []float64 {
	if cap <= 0 {
		cap = 60
	}
	ring = append(ring, v)
	if len(ring) > cap {
		ring = ring[len(ring)-cap:]
	}
	return ring
}
