package dashboard

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
)

// keyPress builds a KeyPressMsg for a single printable key.
func keyPress(s string) tea.KeyPressMsg {
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func newTestModel() model {
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{Name: "OKProv", Kind: "openai-compatible", BaseURL: "http://x/v1", Enabled: true,
			Models: []config.Model{{ID: "mock-alpha", Favourite: true, Probe: "chat"}}},
		{Name: "DownProv", Kind: "openai-compatible", BaseURL: "http://y/v1", Enabled: true},
		{Name: "AuthProv", Kind: "anthropic", BaseURL: "http://z", Enabled: true},
		{Name: "Neuralwatt", Kind: "custom", BaseURL: "http://w/v1", Enabled: false,
			Meters: []config.Meter{{Name: "Energy", Unit: "kWh", Kind: "manual", Used: 231.5, Cap: 1000, Reset: "monthly:1"}}},
	}
	st := state.New()
	now := time.Now()
	st.RecordCheck("OKProv", state.CheckResult{Status: "ok", LatencyMs: 100, CheckedAt: now}, 60)
	st.RecordCheck("AuthProv", state.CheckResult{Status: "account", Reason: "auth: HTTP 401", CheckedAt: now}, 60)
	st.RecordCheck("DownProv", state.CheckResult{Status: "down", Reason: "timeout", CheckedAt: now}, 60)
	st.RecordModelCheck("OKProv", "mock-alpha", state.CheckResult{Status: "ok", LatencyMs: 98, CheckedAt: now}, 60)
	st.SetMeter("Neuralwatt", "Energy", 231.5)

	palette, _ := theme.Load("sstop", "")
	m := New(cfg, st)
	m.width = 120
	m.height = 40
	_ = palette // New already loads the palette
	return m
}

func TestPanelNames(t *testing.T) {
	if panelNames[panelStatus] != "status" || panelNames[panelStats] != "stats" {
		t.Errorf("panel names: %v", panelNames)
	}
	if b, k, a := panelTitleSegs(panelStatus); b+":"+k+":"+a != ":s:tatus" {
		t.Errorf("status segs: %q %q %q", b, k, a)
	}
	if b, k, a := panelTitleSegs(panelStats); b+":"+k+":"+a != "s:t:ats" {
		t.Errorf("stats segs: %q %q %q", b, k, a)
	}
}

func TestMaxSelection(t *testing.T) {
	m := newTestModel()
	if got := m.maxSelection(panelStatus); got != 3 {
		t.Errorf("status maxSelection: got %d want 3", got)
	}
	if got := m.maxSelection(panelFavourites); got != 0 {
		t.Errorf("favourites maxSelection: got %d want 0", got)
	}
}

func TestMoveSelectionClamps(t *testing.T) {
	m := newTestModel()
	m.moveSelection(-10)
	if m.sel[panelStatus] != 0 {
		t.Errorf("sel after -10: got %d want 0", m.sel[panelStatus])
	}
	m.moveSelection(100)
	if m.sel[panelStatus] != 3 {
		t.Errorf("sel after +100: got %d want 3", m.sel[panelStatus])
	}
	// Selection past the fold must stay visible.
	if m.sel[panelStatus] < m.scroll[panelStatus] {
		t.Error("selection scrolled out of view")
	}
}

func TestScrollClampsBothEnds(t *testing.T) {
	pal, _ := theme.Load("sstop", "")
	lines := []string{"a", "b", "c", "d", "e"}
	// Negative offset clamps to 0 at render.
	got := renderScrollable(lines, -5, 3, pal)
	if !strings.HasPrefix(got, "a") {
		t.Errorf("negative offset should clamp to top: %q", got)
	}
	// Past-end offset clamps to last window.
	got = renderScrollable(lines, 999, 3, pal)
	if !strings.Contains(got, "e") {
		t.Errorf("past-end offset should clamp to bottom: %q", got)
	}
}

func TestCycleViewOrder(t *testing.T) {
	m := newTestModel()
	m.st.UI.View = "full"
	m = m.cycleView()
	if m.st.UI.View != "compact" {
		t.Errorf("after full: got %q want compact", m.st.UI.View)
	}
	m = m.cycleView()
	if m.st.UI.View != "status-only" {
		t.Errorf("after compact: got %q want status-only", m.st.UI.View)
	}
	m = m.cycleView()
	if m.st.UI.View != "stats-only" {
		t.Errorf("after status-only: got %q want stats-only", m.st.UI.View)
	}
	m = m.cycleView()
	if m.st.UI.View != "full" {
		t.Errorf("wrap: got %q want full", m.st.UI.View)
	}
}

func TestCycleViewIncludesUserViews(t *testing.T) {
	m := newTestModel()
	m.cfg.Views = []config.View{{Name: "mine", Panels: []string{"stats"}, Arrangement: "stack"}}
	m.st.UI.View = "stats-only"
	m = m.cycleView()
	if m.st.UI.View != "mine" {
		t.Errorf("user view not in cycle: got %q want mine", m.st.UI.View)
	}
	m = m.cycleView()
	if m.st.UI.View != "full" {
		t.Errorf("wrap after user view: got %q want full", m.st.UI.View)
	}
}

func TestActiveViewFallback(t *testing.T) {
	m := newTestModel()
	m.st.UI.View = "nonexistent"
	v := m.activeViewDef()
	if v.Name != "full" {
		t.Errorf("unknown view should fall back to full, got %q", v.Name)
	}
}

func TestUserViewReplacesBuiltin(t *testing.T) {
	m := newTestModel()
	m.cfg.Views = []config.View{{Name: "full", Panels: []string{"stats"}, Arrangement: "stack"}}
	m.st.UI.View = "full"
	v := m.activeViewDef()
	if len(v.Panels) != 1 || v.Panels[0] != "stats" {
		t.Errorf("user view should replace builtin: %+v", v)
	}
}

func TestRenderStatusPane(t *testing.T) {
	m := newTestModel()
	got := m.renderStatusPane(80, 10, false)
	for _, want := range []string{"OKProv", "DownProv", "AuthProv", "Neuralwatt"} {
		if !strings.Contains(got, want) {
			t.Errorf("status pane missing %s", want)
		}
	}
}

func TestRenderStatusPaneEmpty(t *testing.T) {
	m := newTestModel()
	m.cfg.Providers = nil
	got := m.renderStatusPane(80, 10, false)
	if !strings.Contains(got, "no providers") {
		t.Errorf("empty status pane: %q", got)
	}
}

func TestUsagePaneShowsDisabledProviderMeter(t *testing.T) {
	m := newTestModel()
	got := m.renderUsagePane(80, 30, false)
	if !strings.Contains(got, "Neuralwatt") {
		t.Error("disabled provider must still render in usage pane")
	}
	if !strings.Contains(got, "231.5") {
		t.Error("meter value missing")
	}
	if !strings.Contains(got, "kWh") {
		t.Error("meter unit missing")
	}
}

func TestUsagePaneCompactDropsSpacers(t *testing.T) {
	m := newTestModel()
	full := strings.TrimRight(m.renderUsagePane(80, 100, false), "\n")
	compact := strings.TrimRight(m.renderUsagePane(80, 100, true), "\n")
	// Non-compact has interior blank separator lines; compact has none.
	if !strings.Contains(full, "\n\n") {
		t.Error("non-compact should contain interior blank lines")
	}
	if strings.Contains(compact, "\n\n") {
		t.Error("compact must drop spacer lines")
	}
}

func TestRenderStatsPaneNoData(t *testing.T) {
	m := newTestModel()
	m.st = state.New()
	got := m.renderStatsPane(60, 10, false)
	if !strings.Contains(got, "no data yet") {
		t.Errorf("stats pane empty: %q", got)
	}
}

func TestStatsRowMath(t *testing.T) {
	m := newTestModel()
	m.st = state.New()
	// Synthetic 100-check history: 90 ok at 100ms, 10 down at 900ms.
	now := time.Now()
	for i := 0; i < 100; i++ {
		if i%10 == 9 {
			m.st.RecordCheck("P", state.CheckResult{Status: "down", LatencyMs: 900, CheckedAt: now}, 100)
		} else {
			m.st.RecordCheck("P", state.CheckResult{Status: "ok", LatencyMs: 100, CheckedAt: now}, 100)
		}
	}
	m.cfg.Providers = []config.Provider{{Name: "P", Enabled: true}}
	rows := m.statsRows()
	if len(rows) != 1 {
		t.Fatalf("rows: got %d", len(rows))
	}
	r := rows[0]
	if r.checks != 100 || r.okPct != 90 || r.down != 10 {
		t.Errorf("counters: %+v", r)
	}
	if r.p50 != 100 {
		t.Errorf("p50: got %v want 100", r.p50)
	}
	if r.p95 != 900 {
		t.Errorf("p95: got %v want 900", r.p95)
	}
}

func TestStatsSort(t *testing.T) {
	rows := []statsRow{
		{name: "a", checks: 10, okPct: 50, p50: 100},
		{name: "b", checks: 5, okPct: 90, p50: 200},
		{name: "c", checks: 20, okPct: 70, p50: 50},
	}
	asc := sortStats(rows, "checks", 1)
	if asc[0].checks != 5 || asc[2].checks != 20 {
		t.Errorf("asc: %+v", asc)
	}
	desc := sortStats(rows, "checks", 2)
	if desc[0].checks != 20 || desc[2].checks != 5 {
		t.Errorf("desc: %+v", desc)
	}
	def := sortStats(rows, "checks", 0)
	if def[0].checks != 10 {
		t.Errorf("default should preserve order: %+v", def)
	}
}

func TestPercentile(t *testing.T) {
	ring := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if p50 := percentile(ring, 50); p50 < 40 || p50 > 60 {
		t.Errorf("p50: got %v", p50)
	}
	if p95 := percentile(ring, 95); p95 < 90 {
		t.Errorf("p95: got %v", p95)
	}
	if percentile(nil, 50) != 0 {
		t.Error("empty ring should be 0")
	}
}

// TestPercentileUnsorted: percentile must sort internally — an unsorted ring
// is the normal case (rings are append-ordered by time, not magnitude).
func TestPercentileUnsorted(t *testing.T) {
	ring := []float64{100, 10, 50, 90, 20}
	if p50 := percentile(ring, 50); p50 != 50 {
		t.Errorf("p50 of unsorted [100 10 50 90 20]: got %v want 50", p50)
	}
	if p95 := percentile(ring, 95); p95 != 100 {
		t.Errorf("p95: got %v want 100", p95)
	}
}

// TestShouldBell: alert bell rings only on transitions INTO down (M23).
func TestShouldBell(t *testing.T) {
	cases := []struct {
		prev, next string
		want       bool
	}{
		{"ok", "down", true},
		{"account", "down", true},
		{"unknown", "down", true},
		{"down", "down", false}, // sustained down: no repeat bell
		{"down", "ok", false},
		{"ok", "account", false},
		{"ok", "ok", false},
	}
	for _, tc := range cases {
		if got := shouldBell(tc.prev, tc.next); got != tc.want {
			t.Errorf("shouldBell(%q,%q): got %v want %v", tc.prev, tc.next, got, tc.want)
		}
	}
}

// TestCheckInFlightStateMachine: regression guard for the value-receiver bug —
// after Update('c'), checking must be true and pendingCount must equal the
// job count on the RETURNED model.
func TestCheckInFlightStateMachine(t *testing.T) {
	m := newTestModel()
	m.cfg.Settings.ProbeTimeout = 1 // don't actually wait on network
	tm, cmd := m.handleKey(keyPress("c"))
	m2 := tm.(model)
	if !m2.checking {
		t.Fatal("checking must be true after 'c'")
	}
	// 3 enabled providers + 1 favourite = 4 jobs.
	if m2.pendingCount != 4 {
		t.Errorf("pendingCount: got %d want 4", m2.pendingCount)
	}
	if cmd == nil {
		t.Fatal("a check command must be returned")
	}
	// Second 'c' while in flight must be a no-op (no double token spend).
	tm3, cmd3 := m2.handleKey(keyPress("c"))
	m3 := tm3.(model)
	if cmd3 != nil {
		t.Error("re-check while in flight must not enqueue again")
	}
	if m3.pendingCount != 4 {
		t.Errorf("pendingCount changed: %d", m3.pendingCount)
	}
}

func TestRenderScrollable(t *testing.T) {
	pal, _ := theme.Load("sstop", "")
	lines := []string{"a", "b", "c", "d", "e"}
	got := renderScrollable(lines, 0, 3, pal)
	if !strings.Contains(got, "a") || !strings.Contains(got, "c") {
		t.Errorf("scrollable(0,3): %q", got)
	}
	if strings.Contains(got, "d\n") {
		t.Errorf("scrollable(0,3) should not show line 4: %q", got)
	}
	// Scrollbar appears on overflow.
	if !strings.Contains(got, "█") && !strings.Contains(got, "░") {
		t.Error("overflow should render scrollbar")
	}
	got = renderScrollable(lines, 100, 3, pal)
	if !strings.Contains(got, "e") {
		t.Errorf("scrollable clamp: %q", got)
	}
}

func TestResetDescription(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	got, urgency := resetDescription("monthly:1", now)
	if !strings.Contains(got, "resets in") {
		t.Errorf("monthly:1 from Aug 19: %q", got)
	}
	if urgency != 0 {
		t.Errorf("monthly:1 is 13d out, urgency should be 0, got %d", urgency)
	}
	if got, _ := resetDescription("never", now); got != "" {
		t.Errorf("never should be empty: %q", got)
	}
	// Overdue date in the past must be flagged.
	got, urgency = resetDescription("date:2026-08-01", now)
	if urgency != -1 || !strings.Contains(got, "overdue") {
		t.Errorf("past date should be overdue: %q urgency=%d", got, urgency)
	}
}

func TestOverlayHelpRenders(t *testing.T) {
	m := newTestModel()
	m.ov = m.newHelpOverlay()
	frame := m.render()
	if !strings.Contains(frame, "quit") {
		t.Error("help overlay should render keymap")
	}
}

func TestInspectShowsRecentErrors(t *testing.T) {
	m := newTestModel()
	m.st.RecordCheck("OKProv", state.CheckResult{Status: "down", Reason: "boom", CheckedAt: time.Now()}, 60)
	m.focused = panelStatus
	m.sel[panelStatus] = 0
	// OKProv sorts first by name? AuthProv < DownProv < Neuralwatt < OKProv alphabetically.
	// Select OKProv explicitly.
	for i, p := range m.sortedProviders() {
		if p.Name == "OKProv" {
			m.sel[panelStatus] = i
		}
	}
	text := m.inspectText()
	if !strings.Contains(text, "boom") {
		t.Errorf("inspect should include recent error reason: %q", text)
	}
}

func TestMenuOverlaysOpen(t *testing.T) {
	m := newTestModel()
	for _, p := range []panelID{panelStatus, panelUsage, panelFavourites, panelStats} {
		ov := m.newMenuOverlay(p)
		if ov.kind != overlayMenu || len(ov.menuItems) == 0 {
			t.Errorf("panel %d: menu empty", p)
		}
	}
}

func TestHeadingBracketsInRender(t *testing.T) {
	m := newTestModel()
	frame := ansi.Strip(m.render())
	for _, b := range []string{"[s]tatus", "[u]sage", "[f]avourites", "s[t]ats"} {
		if !strings.Contains(frame, b) {
			t.Errorf("frame missing heading %q", b)
		}
	}
}

func TestStackLayoutNarrow(t *testing.T) {
	m := newTestModel()
	m.width = 80 // < 100 forces stack
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "[s]tatus") {
		t.Error("stack render missing status pane")
	}
}

func TestViewPanelsOmission(t *testing.T) {
	m := newTestModel()
	m.st.UI.View = "stats-only"
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "s[t]ats") {
		t.Error("stats-only view should render stats")
	}
	if strings.Contains(frame, "[s]tatus") {
		t.Error("stats-only view must omit status pane")
	}
}

// TestFrameWidthInvariant: no rendered line may exceed the terminal width —
// over-width lines wrap and shift every corner (the "corners off" defect).
func TestFrameWidthInvariant(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 160} {
		m := newTestModel()
		m.width, m.height = w, 40
		for _, l := range strings.Split(m.render(), "\n") {
			if got := lipgloss.Width(l); got > w {
				t.Errorf("width %d: line is %d cells wide: %.40q", w, got, l)
			}
		}
	}
	// And with the wizard popup open (first-run path).
	m := New(config.Default(), state.New())
	m.width, m.height = 100, 40
	for _, l := range strings.Split(m.render(), "\n") {
		if got := lipgloss.Width(l); got > 100 {
			t.Errorf("popup: line is %d cells wide: %.40q", got, l)
		}
	}
}
