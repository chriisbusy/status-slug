package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
)

func newTestModel() model {
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{Name: "OKProv", Kind: "openai-compatible", BaseURL: "http://x/v1", Enabled: true},
		{Name: "DownProv", Kind: "openai-compatible", BaseURL: "http://y/v1", Enabled: true},
		{Name: "AuthProv", Kind: "anthropic", BaseURL: "http://z", Enabled: true},
	}
	st := state.New()
	now := time.Now()
	st.RecordCheck("OKProv", state.CheckResult{Status: "ok", LatencyMs: 100, CheckedAt: now}, 60)
	st.RecordCheck("AuthProv", state.CheckResult{Status: "account", Reason: "auth: HTTP 401", CheckedAt: now}, 60)
	st.RecordCheck("DownProv", state.CheckResult{Status: "down", Reason: "timeout", CheckedAt: now}, 60)

	palette, _ := theme.Load("sstop", "")
	m := model{
		cfg:     cfg,
		st:      st,
		palette: palette,
		width:   120,
		height:  40,
	}
	return m
}

func TestPaneNames(t *testing.T) {
	if panelNames[panelStatus] != "status" {
		t.Errorf("panelStatus name: %q", panelNames[panelStatus])
	}
	if panelNames[panelStats] != "stats" {
		t.Errorf("panelStats name: %q", panelNames[panelStats])
	}
}

func TestMaxSelection(t *testing.T) {
	m := newTestModel()
	if got := m.maxSelection(panelStatus); got != 2 {
		t.Errorf("status maxSelection: got %d want 2", got)
	}
}

func TestMoveSelectionClamps(t *testing.T) {
	m := newTestModel()
	m.moveSelection(-10)
	if m.sel[panelStatus] != 0 {
		t.Errorf("sel after -10: got %d want 0", m.sel[panelStatus])
	}
	m.moveSelection(100)
	if m.sel[panelStatus] != 2 {
		t.Errorf("sel after +100: got %d want 2", m.sel[panelStatus])
	}
}

func TestCycleView(t *testing.T) {
	m := newTestModel()
	m.st.UI.View = "full"
	m2 := m.cycleView()
	if m2.st.UI.View != "compact" {
		t.Errorf("cycleView: got %q want compact", m2.st.UI.View)
	}
	m3 := m2.cycleView()
	if m3.st.UI.View != "status-only" {
		t.Errorf("cycleView 2: got %q want status-only", m3.st.UI.View)
	}
	// Wraps around.
	m4 := model{cfg: m.cfg, st: m.st}
	m4.st.UI.View = "stats-only"
	m5 := m4.cycleView()
	if m5.st.UI.View != "full" {
		t.Errorf("cycleView wrap: got %q want full", m5.st.UI.View)
	}
}

func TestActiveViewFallback(t *testing.T) {
	m := newTestModel()
	m.st.UI.View = "nonexistent"
	v := m.activeView()
	if v.Name != "full" {
		t.Errorf("unknown view should fall back to full, got %q", v.Name)
	}
}

func TestRenderStatusPane(t *testing.T) {
	m := newTestModel()
	got := m.renderStatusPane(80, 10)
	if !strings.Contains(got, "OKProv") {
		t.Error("status pane missing OKProv")
	}
	if !strings.Contains(got, "DownProv") {
		t.Error("status pane missing DownProv")
	}
}

func TestRenderStatusPaneEmpty(t *testing.T) {
	m := newTestModel()
	m.cfg.Providers = nil
	got := m.renderStatusPane(80, 10)
	if !strings.Contains(got, "no providers") {
		t.Errorf("empty status pane: %q", got)
	}
}

func TestRenderStatsPaneNoData(t *testing.T) {
	m := newTestModel()
	m.st = state.New()
	got := m.renderStatsPane(60, 10)
	if !strings.Contains(got, "no data yet") {
		t.Errorf("stats pane empty: %q", got)
	}
}

func TestRenderStatsPaneWithData(t *testing.T) {
	m := newTestModel()
	// Add enough checks for meaningful percentiles.
	for i := 0; i < 50; i++ {
		m.st.RecordCheck("OKProv", stateCheck("ok", float64(100+i)), 60)
	}
	got := m.renderStatsPane(80, 20)
	if !strings.Contains(got, "OKProv") {
		t.Error("stats pane missing provider name")
	}
	if !strings.Contains(got, "checks") {
		t.Error("stats pane missing header")
	}
}

func stateCheck(status string, latency float64) state.CheckResult {
	return state.CheckResult{Status: status, LatencyMs: latency, CheckedAt: time.Now()}
}

func TestPercentile(t *testing.T) {
	ring := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	p50 := percentile(ring, 50)
	if p50 < 40 || p50 > 60 {
		t.Errorf("p50 of 10..100: got %v", p50)
	}
	p95 := percentile(ring, 95)
	if p95 < 90 {
		t.Errorf("p95 of 10..100: got %v", p95)
	}
	if percentile(nil, 50) != 0 {
		t.Error("p50 of empty ring should be 0")
	}
}

func TestRenderScrollable(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	got := renderScrollable(lines, 0, 3)
	if !strings.Contains(got, "a") || !strings.Contains(got, "c") {
		t.Errorf("scrollable(0,3): %q", got)
	}
	if strings.Contains(got, "d") {
		t.Errorf("scrollable(0,3) should not show line 4: %q", got)
	}
	// Clamp offset.
	got = renderScrollable(lines, 100, 3)
	if !strings.Contains(got, "e") {
		t.Errorf("scrollable clamp: %q", got)
	}
}

func TestResetDescription(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	got := resetDescription("monthly:1", now)
	if !strings.Contains(got, "resets in") {
		t.Errorf("monthly:1 from Aug 19: %q", got)
	}
	got = resetDescription("never", now)
	if got != "" {
		t.Errorf("never should be empty: %q", got)
	}
}

func TestRenderOverlayHelp(t *testing.T) {
	m := newTestModel()
	m.overlay = overlayHelp
	m.overlayData = helpText
	frame := m.render()
	if !strings.Contains(frame, "Help") {
		t.Error("help overlay not rendered")
	}
}
