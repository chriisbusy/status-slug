// Package serve exposes status and usage snapshots over loopback HTTP.
package serve

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/state"
)

// Snapshot aliases so serve can build the same payload as cmd/sslug
// without an import cycle (cmd imports serve, not the reverse).
type Snapshot struct {
	Schema    int                `json:"schema"`
	Providers []SnapshotProvider `json:"providers"`
}

type SnapshotProvider struct {
	Name      string          `json:"name"`
	Label     string          `json:"label,omitempty"`
	Status    string          `json:"status"`
	Reason    string          `json:"reason,omitempty"`
	LatencyMs float64         `json:"latency_ms,omitempty"`
	CheckedAt time.Time       `json:"checked_at,omitempty"`
	Models    []SnapshotModel `json:"models,omitempty"`
	Meters    []SnapshotMeter `json:"meters,omitempty"`
}

type SnapshotModel struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	LatencyMs float64   `json:"latency_ms,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
}

type SnapshotMeter struct {
	Name   string    `json:"name"`
	Unit   string    `json:"unit"`
	Value  float64   `json:"value"`
	Cap    float64   `json:"cap,omitempty"`
	Reset  string    `json:"reset,omitempty"`
	SetAt  time.Time `json:"set_at,omitempty"`
	Source string    `json:"source"`
}

// BuildSnapshot assembles the status snapshot from config + state.
func BuildSnapshot(cfg config.Config, st *state.File) Snapshot {
	snap := Snapshot{Schema: 1}
	for _, p := range cfg.Providers {
		sp := SnapshotProvider{Name: p.Name, Label: p.Label, Status: "unknown"}
		if ps := st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
			sp.Status = ps.LastCheck.Status
			sp.Reason = ps.LastCheck.Reason
			sp.LatencyMs = ps.LastCheck.LatencyMs
			sp.CheckedAt = ps.LastCheck.CheckedAt
		}
		for _, m := range p.Models {
			sm := SnapshotModel{ID: m.ID, Status: "unknown"}
			if ps := st.Providers[p.Name]; ps != nil {
				if ms := ps.Models[m.ID]; ms != nil && ms.LastCheck != nil {
					sm.Status = ms.LastCheck.Status
					sm.Reason = ms.LastCheck.Reason
					sm.LatencyMs = ms.LastCheck.LatencyMs
					sm.CheckedAt = ms.LastCheck.CheckedAt
				}
			}
			sp.Models = append(sp.Models, sm)
		}
		for _, m := range p.Meters {
			sm := SnapshotMeter{Name: m.Name, Unit: m.Unit, Reset: m.Reset, Source: m.Kind}
			if m.Cap > 0 {
				sm.Cap = m.Cap
			}
			if mv := st.GetMeter(p.Name, m.Name); mv != nil {
				sm.Value = mv.Value
				sm.SetAt = mv.SetAt
			} else {
				sm.Value = m.Used
			}
			sp.Meters = append(sp.Meters, sm)
		}
		snap.Providers = append(snap.Providers, sp)
	}
	return snap
}

// NewMux returns the HTTP handler for GET /status.json and GET /usage.json.
func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/status.json", func(w http.ResponseWriter, r *http.Request) {
		cfg, st := loadBoth()
		writeJSON(w, BuildSnapshot(cfg, st))
	})
	mux.HandleFunc("/usage.json", func(w http.ResponseWriter, r *http.Request) {
		cfg, st := loadBoth()
		snaps := provider.MoshiBuild(cfg, st, time.Now())
		writeJSON(w, snaps)
	})
	return mux
}

// ListenAndServe binds to addr (must be loopback) and serves mux.
func ListenAndServe(addr string, mux *http.ServeMux) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return &net.OpError{Op: "listen", Err: errRefuseNonLoopback{addr}}
	}
	return http.ListenAndServe(addr, mux)
}

type errRefuseNonLoopback struct{ addr string }

func (e errRefuseNonLoopback) Error() string {
	return "sslug serve: refusing to bind non-loopback address " + e.addr
}

func loadBoth() (config.Config, *state.File) {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	st, err := state.Load()
	if err != nil {
		st = state.New()
	}
	return cfg, st
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
