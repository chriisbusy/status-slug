package dashboard

import (
	"fmt"
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
	m.cfg.Views = []config.View{{Name: "mine", Panels: []string{"stats"}, TopRatio: 0.33, LeftRatio: 0.37, UsageRatio: 0.46}}
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
	m.cfg.Views = []config.View{{Name: "full", Panels: []string{"stats"}, TopRatio: 0.33, LeftRatio: 0.37, UsageRatio: 0.46}}
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

func TestStatusPaneShowsGraphGaugesAndProviders(t *testing.T) {
	m := newTestModel()
	got := ansi.Strip(m.renderStatusPane(90, 20, false))
	for _, want := range []string{
		"600ms",
		"errors",
		"provider/model",
		"OKProv",
		"AuthProv",
		"DownProv",
		"Neuralwatt",
		"▰",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status pane missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderStatusPaneEmptyLeavesActionForBorder(t *testing.T) {
	m := newTestModel()
	m.cfg.Providers = nil
	got := ansi.Strip(m.renderStatusPane(80, 10, false))
	if strings.Contains(got, "no providers") {
		t.Errorf("empty action belongs on pane border, not content: %q", got)
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

func TestUsagePaneShowsSquareCellMeter(t *testing.T) {
	m := newTestModel()
	meter := m.cfg.Providers[3].Meters[0]
	got := ansi.Strip(strings.Join(m.meterLines("Neuralwatt", meter, 80, false), "\n"))
	for _, want := range []string{"Neuralwatt · Energy", "231.5 / 1000 kWh", "23%", "manual"} {
		if !strings.Contains(got, want) {
			t.Errorf("usage meter missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "■") {
		t.Errorf("usage meter must use square cells:\n%s", got)
	}
	if len(strings.Split(got, "\n")) != 3 {
		t.Fatalf("capped meter should render exactly three lines:\n%s", got)
	}
}

func TestRenderStatsPaneWithoutChecksShowsUnknownRows(t *testing.T) {
	m := newTestModel()
	m.st = state.New()
	got := ansi.Strip(m.renderStatsPane(60, 10, false))
	for _, want := range []string{"program", "OKProv", "mock-alpha"} {
		if !strings.Contains(got, want) {
			t.Errorf("stats pane missing configured unknown row %q: %q", want, got)
		}
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

func TestSparkGraphStylesDiffer(t *testing.T) {
	values := []float64{40, 95, 180, 130, 260, 115}
	tty := Spark(values, 10, "tty")
	block := Spark(values, 10, "block")
	braille := Spark(values, 10, "braille")
	if tty == block || tty == braille || block == braille {
		t.Fatalf("graph styles should differ: tty=%q block=%q braille=%q", tty, block, braille)
	}
}

func TestFavouritesPaneShowsCockpitHistoryAndP95(t *testing.T) {
	m := newTestModel()
	now := time.Now()
	for _, ms := range []float64{40, 95, 180, 130, 260, 115} {
		m.st.RecordModelCheck("OKProv", "mock-alpha", state.CheckResult{Status: "ok", LatencyMs: ms, CheckedAt: now}, 60)
	}
	got := ansi.Strip(m.renderFavouritesPane(80, 20, false))
	for _, want := range []string{"model", "graph", "p95", "OKProv", "mock-alpha"} {
		if !strings.Contains(got, want) {
			t.Errorf("favourites pane missing %q in:\n%s", want, got)
		}
	}
	if !strings.ContainsAny(got, "⡀⣀⣤⣶⣿▁▂▃▄▅▆▇█.-=+*#%@") {
		t.Errorf("favourites pane missing visible history glyphs:\n%s", got)
	}
}

func TestStatsColumnsKeepProcessGrammarAtNarrowWidth(t *testing.T) {
	_, keys := statsColumnsForWidth(38)
	present := map[string]bool{}
	for _, key := range keys {
		present[key] = true
	}
	for _, want := range []string{"name", "latency", "history", "p95", "status"} {
		if !present[want] {
			t.Errorf("stats column %q missing; keys=%v", want, keys)
		}
	}
}

func TestStatsPaneCompactRenderShowsProcessColumns(t *testing.T) {
	m := newTestModel()
	got := ansi.Strip(m.renderStatsPane(38, 10, false))
	for _, want := range []string{"program", "latency", "graph", "p95", "OKProv"} {
		if !strings.Contains(got, want) {
			t.Errorf("compact stats pane missing %q in:\n%s", want, got)
		}
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

func TestFullViewUsesConfiguredRatios(t *testing.T) {
	full := builtinViews()[0]
	if full.Name != "full" || full.TopRatio != 0.33 || full.LeftRatio != 0.37 || full.UsageRatio != 0.46 {
		t.Fatalf("full view ratios: %+v", full)
	}
}

func TestHeaderKeepsExactSSLUGWordmark(t *testing.T) {
	m := newTestModel()
	rows, _ := m.headerRows()
	got := ansi.Strip(strings.Join(rows, "\n"))
	for _, want := range theme.ArtLines {
		if !strings.Contains(got, want) {
			t.Fatalf("header missing wordmark row %q:\n%s", want, got)
		}
	}
}

func TestFooterUsesCompactWholeActions(t *testing.T) {
	m := newTestModel()
	got := ansi.Strip(m.renderFooter())
	for _, want := range []string{"⏎ probe", "c all", "s actions", "p preset"} {
		if !strings.Contains(got, want) {
			t.Fatalf("footer missing compact action %q: %q", want, got)
		}
	}
	if ansi.StringWidth(m.renderFooter()) > m.width {
		t.Fatalf("footer exceeds width %d: %q", m.width, got)
	}
}

func TestHeaderClickRegionsOpenMenu(t *testing.T) {
	m := newTestModel()
	_, regions := m.headerRows()
	var menu hitRegion
	for _, r := range regions {
		if r.kind == "menu" {
			menu = r
			break
		}
	}
	if menu.w == 0 {
		t.Fatalf("menu header region missing: %+v", regions)
	}
	next, _ := m.handleClick(tea.Mouse{X: menu.x, Y: menu.y, Button: tea.MouseLeft})
	got := next.(model)
	if got.ov.kind != overlayMenu {
		t.Fatalf("menu header click did not open menu: %+v", got.ov)
	}
}

func TestFavouritesPaneRowsFitNarrowWidths(t *testing.T) {
	m := newTestModel()
	for _, w := range []int{45, 55, 67} {
		got := ansi.Strip(m.renderFavouritesPane(w, 20, false))
		for i, line := range strings.Split(got, "\n") {
			if width := ansi.StringWidth(line); width > w {
				t.Fatalf("width %d line %d overflows: got %d cells in %q", w, i, width, line)
			}
		}
	}
}

func TestCompactStatsHeaderClickSortsRenderedColumn(t *testing.T) {
	m := newTestModel()
	m.zoomed = true
	m.focused = panelStats
	m.width = 51
	m.height = 20
	contentWidth := m.width - 3
	columns, keys := statsColumnsForWidth(contentWidth)
	p95Start := 0
	for index, key := range keys {
		if key == "p95" {
			break
		}
		p95Start += columns[index].Width
	}
	next, _ := m.handleClick(tea.Mouse{X: 1 + p95Start, Y: 3, Button: tea.MouseLeft})
	got := next.(model)
	if got.prefs.statsSort != "p95" {
		t.Fatalf("compact stats click sorted %q, want p95", got.prefs.statsSort)
	}
}

func TestStatsHeaderClickUsesFullLayoutAtBoundaryWidth(t *testing.T) {
	m := newTestModel()
	m.zoomed = true
	m.focused = panelStats
	m.width = 92
	m.height = 20
	contentWidth := m.width - 3
	cols, keys := statsColumnsForWidth(contentWidth)
	p95Start := 0
	for i, key := range keys {
		if key == "p95" {
			break
		}
		p95Start += cols[i].Width
	}
	next, _ := m.handleClick(tea.Mouse{X: 1 + p95Start, Y: 3, Button: tea.MouseLeft})
	got := next.(model)
	if got.prefs.statsSort != "p95" {
		t.Fatalf("boundary full stats click sorted %q, want p95", got.prefs.statsSort)
	}
}

func TestStatusReasonSummaryDoesNotTreatEveryFiveAsServerFailure(t *testing.T) {
	got := statusReasonSummary("down", "connection refused to 10.0.0.5", "custom", "http://10.0.0.5")
	if strings.Contains(got, "provider/server failure") {
		t.Fatalf("digit 5 in address should not become server failure: %q", got)
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

func TestPaneTitlesUseEmbeddedActivationLetters(t *testing.T) {
	m := newTestModel()
	frame := ansi.Strip(m.render())
	for _, title := range []string{"status", "stats"} {
		if !strings.Contains(frame, title) {
			t.Errorf("frame missing heading %q", title)
		}
	}
	for _, bracketed := range []string{"[s]tatus", "[u]sage", "[f]avourites", "s[t]ats"} {
		if strings.Contains(frame, bracketed) {
			t.Errorf("frame retained bracketed heading %q", bracketed)
		}
	}
}

func TestProgressiveLayoutAtNarrowSize(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 80, 24
	frame := ansi.Strip(m.render())
	for _, title := range []string{"status", "stats"} {
		if !strings.Contains(frame, title) {
			t.Fatalf("80x24 frame missing %s pane", title)
		}
	}
}

func TestProgressivePaneAdmission(t *testing.T) {
	cases := []struct {
		width, height int
		want          []panelID
	}{
		{60, 18, []panelID{panelStatus}},
		{80, 24, []panelID{panelStatus, panelStats}},
		{90, 27, []panelID{panelStatus, panelStats, panelUsage}},
		{140, 45, []panelID{panelStatus, panelStats, panelUsage, panelFavourites}},
	}
	for _, tc := range cases {
		m := newTestModel()
		m.width, m.height = tc.width, tc.height
		got := m.admittedPanels()
		if len(got) != len(tc.want) {
			t.Fatalf("%dx%d admitted %v, want %v", tc.width, tc.height, got, tc.want)
		}
		for index := range got {
			if got[index] != tc.want[index] {
				t.Fatalf("%dx%d admitted %v, want %v", tc.width, tc.height, got, tc.want)
			}
		}
	}
}

func TestSplitRatiosChangeSameTerminalGeometry(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 140, 45
	before := m.paneLayout()
	view := m.activeViewDef()
	view.TopRatio, view.LeftRatio, view.UsageRatio = 0.50, 0.55, 0.68
	m.upsertUserView(view)
	after := m.paneLayout()
	if len(before) != len(after) {
		t.Fatalf("pane count changed: before=%v after=%v", before, after)
	}
	same := true
	for index := range before {
		if before[index] != after[index] {
			same = false
			break
		}
	}
	if same {
		t.Fatalf("ratio change did not alter geometry: %v", before)
	}
}

func TestSplitDragUpdatesActiveView(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 140, 45
	m.applySplitDrag("left", 77, headerLines+20)
	got := m.activeViewDef()
	if got.LeftRatio < 0.54 || got.LeftRatio > 0.56 {
		t.Fatalf("left ratio after drag = %.3f, want about .55", got.LeftRatio)
	}
}

func TestViewPanelsOmission(t *testing.T) {
	m := newTestModel()
	m.st.UI.View = "stats-only"
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "stats") {
		t.Error("stats-only view should render stats")
	}
	if strings.Contains(frame, "status") {
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

func TestResponsiveFrameSweep(t *testing.T) {
	for width := 48; width <= 200; width++ {
		for height := 14; height <= 60; height++ {
			m := newTestModel()
			m.width, m.height = width, height
			frame := m.render()
			lines := strings.Split(frame, "\n")
			if len(lines) != height {
				t.Fatalf("%dx%d rendered %d rows", width, height, len(lines))
			}
			for row, line := range lines {
				if got := ansi.StringWidth(line); got != width {
					t.Fatalf("%dx%d row %d width = %d", width, height, row, got)
				}
			}
			plain := ansi.Strip(frame)
			if strings.Contains(plain, "…") {
				t.Fatalf("%dx%d rendered an ellipsis", width, height)
			}
			for _, wordmarkRow := range theme.ArtLines {
				if !strings.Contains(plain, wordmarkRow) {
					t.Fatalf("%dx%d lost wordmark row %q", width, height, wordmarkRow)
				}
			}
		}
	}
}

func TestEveryGraphSurfaceUsesConfiguredStyle(t *testing.T) {
	m := newTestModel()
	now := time.Now()
	for _, latency := range []float64{40, 95, 180, 130, 260, 115} {
		m.st.RecordCheck("OKProv", state.CheckResult{Status: "ok", LatencyMs: latency, CheckedAt: now}, 60)
		m.st.RecordModelCheck("OKProv", "mock-alpha", state.CheckResult{Status: "ok", LatencyMs: latency, CheckedAt: now}, 60)
	}
	outputs := map[string]string{}
	for _, style := range []string{"tty", "block", "braille"} {
		m.cfg.Settings.GraphStyle = style
		outputs[style] = ansi.Strip(m.renderStatusPane(90, 15, false)) +
			ansi.Strip(m.renderFavouritesPane(70, 12, false)) +
			ansi.Strip(m.renderStatsPane(70, 12, false))
	}
	if outputs["tty"] == outputs["block"] || outputs["tty"] == outputs["braille"] || outputs["block"] == outputs["braille"] {
		t.Fatalf("graph surfaces did not change together across styles")
	}
}

func TestStatsScrollbarIsInsetInsideBorder(t *testing.T) {
	m := newTestModel()
	m.cfg.Providers[0].Models = append(m.cfg.Providers[0].Models,
		config.Model{ID: "mock-beta", Favourite: true},
		config.Model{ID: "mock-gamma", Favourite: true},
	)
	m.width, m.height = 80, 24
	m.focused = panelStats
	frame := ansi.Strip(m.render())
	found := false
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasSuffix(line, "█│") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("stats scrollbar thumb did not retain outer border:\n%s", frame)
	}
}

func TestSstopUsesOneActionColour(t *testing.T) {
	m := newTestModel()
	frame := m.render()
	action := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	for _, key := range []string{"m", "p", "c", "?", "s"} {
		if !strings.Contains(frame, action.Render(key)) {
			t.Fatalf("action key %q does not use the shared accent role", key)
		}
	}
}

func TestMouseDragPersistsSplitRatio(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("SSLUG_CONFIG_HOME", configHome)
	m := newTestModel()
	m.width, m.height = 140, 45
	var stats paneRect
	for _, rect := range m.paneLayout() {
		if rect.panel == panelStats {
			stats = rect
			break
		}
	}
	if stats.x == 0 {
		t.Fatal("full layout has no vertical split")
	}
	next, _ := m.handleClick(tea.Mouse{X: stats.x, Y: headerLines + stats.y + 2, Button: tea.MouseLeft})
	dragging := next.(model)
	if dragging.dragSplit != "left" {
		t.Fatalf("drag kind = %q, want left", dragging.dragSplit)
	}
	next, _ = dragging.handleMouseRelease(tea.Mouse{X: 77, Y: headerLines + stats.y + 2, Button: tea.MouseLeft})
	released := next.(model)
	if released.dragSplit != "" {
		t.Fatalf("drag remained active: %q", released.dragSplit)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	view := loaded.FindView("full")
	if view == nil || view.LeftRatio < 0.54 || view.LeftRatio > 0.56 {
		t.Fatalf("persisted full view = %+v, want left ratio about .55", view)
	}
}

func TestStatsKeyboardSelectionStaysInViewport(t *testing.T) {
	m := newTestModel()
	for index := range 8 {
		m.cfg.Providers[0].Models = append(m.cfg.Providers[0].Models, config.Model{
			ID: fmt.Sprintf("extra-%d", index), Favourite: true,
		})
	}
	m.width, m.height = 80, 24
	m.focused = panelStats
	for range 8 {
		m.moveSelection(1)
	}
	visible := m.selectableViewportHeight(panelStats)
	if m.sel[panelStats] >= m.scroll[panelStats]+visible {
		t.Fatalf("selection %d outside viewport [%d,%d)", m.sel[panelStats], m.scroll[panelStats], m.scroll[panelStats]+visible)
	}
}

func TestLowerPaneTitleClickStillOpensMenu(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 140, 45
	for _, rect := range m.paneLayout() {
		if rect.panel != panelStats && rect.panel != panelFavourites {
			continue
		}
		next, _ := m.handleClick(tea.Mouse{
			X: rect.x + 3, Y: headerLines + rect.y, Button: tea.MouseLeft,
		})
		got := next.(model)
		if got.dragSplit != "" || got.ov.kind != overlayMenu {
			t.Fatalf("%s title click: drag=%q overlay=%v", panelNames[rect.panel], got.dragSplit, got.ov.kind)
		}
	}
}

func TestLatencySurfacesRetainAge(t *testing.T) {
	m := newTestModel()
	favourites := ansi.Strip(m.renderFavouritesPane(80, 12, false))
	stats := ansi.Strip(m.renderStatsPane(90, 12, false))
	for name, output := range map[string]string{"favourites": favourites, "stats": stats} {
		if !strings.Contains(output, "age") || !strings.Contains(output, "just") {
			t.Fatalf("%s dropped freshness:\n%s", name, output)
		}
	}
}

func TestNonFocusedPaneScrollUsesItsOwnHeight(t *testing.T) {
	m := newTestModel()
	for index := range 12 {
		m.cfg.Providers[3].Meters = append(m.cfg.Providers[3].Meters, config.Meter{
			Name: fmt.Sprintf("meter-%d", index), Unit: "req", Kind: "manual", Cap: 100,
		})
	}
	m.width, m.height = 140, 45
	m.focused = panelStats
	if m.selectableViewportHeight(panelStats) == m.selectableViewportHeight(panelUsage) {
		t.Fatal("test requires unequal pane heights")
	}
	content := m.renderUsagePane(80, 1<<20, false)
	total := strings.Count(content, "\n") + 1
	want := max(0, total-m.selectableViewportHeight(panelUsage))
	if got := m.maxScroll(panelUsage); got != want {
		t.Fatalf("usage maxScroll = %d, want %d from its own viewport", got, want)
	}
}

func TestLegacyStatsSortPreferencesMigrateToVisibleColumns(t *testing.T) {
	cases := map[string]string{
		"ago":    "age",
		"p50":    "p95",
		"checks": "",
		"ok%":    "",
		"down":   "",
	}
	for legacy, want := range cases {
		stateFile := state.New()
		stateFile.UI.Panels = map[string]string{"stats.sort": legacy, "stats.dir": "asc"}
		got := loadPrefs(stateFile)
		if got.statsSort != want {
			t.Fatalf("legacy stats sort %q migrated to %q, want %q", legacy, got.statsSort, want)
		}
	}
}

func TestInvalidVisualSettingsFallBackWithWarning(t *testing.T) {
	cfg := config.Default()
	cfg.Settings.BorderStyle = "wobbly"
	cfg.Settings.GraphStyle = "pixels"
	cfg.Providers = []config.Provider{{Name: "test", Enabled: true}}
	m := New(cfg, state.New())
	if m.cfg.Settings.BorderStyle != "rounded" || m.cfg.Settings.GraphStyle != "tty" {
		t.Fatalf("invalid settings not normalized: %+v", m.cfg.Settings)
	}
	for _, want := range []string{"unknown border style", "unknown graph style"} {
		if !strings.Contains(m.footer, want) {
			t.Fatalf("footer missing %q warning: %q", want, m.footer)
		}
	}
}

func TestUsageRetainsProviderNoteAndProbeCounters(t *testing.T) {
	m := newTestModel()
	m.cfg.Providers[3].Note = "home lab allocation"
	providerState := m.st.Provider("Neuralwatt")
	providerState.Counters = state.Counters{Checks: 6, OK: 4, Account: 1, Down: 1}
	got := ansi.Strip(m.renderUsagePane(80, 30, false))
	for _, want := range []string{"home lab allocation", "probes 4 ok · 1 account · 1 down"} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage pane missing %q:\n%s", want, got)
		}
	}
}

func TestStatusGroupingChangesProviderOrder(t *testing.T) {
	providers := []*config.Provider{
		{Name: "zeta", Label: "b"},
		{Name: "alpha", Label: "a"},
		{Name: "beta", Label: "b"},
	}
	got := groupedProviders(providers)
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	if strings.Join(names, ",") != "alpha,zeta,beta" {
		t.Fatalf("grouped provider order = %v", names)
	}
}

func TestOverflowScrollbarsExposeArrowsAndThumb(t *testing.T) {
	m := newTestModel()
	for index := range 12 {
		m.cfg.Providers = append(m.cfg.Providers, config.Provider{
			Name: fmt.Sprintf("extra-%02d", index), Enabled: true,
			Models: []config.Model{{ID: fmt.Sprintf("model-%02d", index), Favourite: true}},
		})
	}
	for name, output := range map[string]string{
		"status":     ansi.Strip(m.renderStatusPane(60, 8, false)),
		"usage":      ansi.Strip(m.renderUsagePane(60, 8, false)),
		"favourites": ansi.Strip(m.renderFavouritesPane(60, 8, false)),
	} {
		for _, glyph := range []string{"▲", "█", "▼"} {
			if !strings.Contains(output, glyph) {
				t.Fatalf("%s overflow missing scrollbar %s:\n%s", name, glyph, output)
			}
		}
	}
}

func TestPaneSplitConfirmationExpiresBySequence(t *testing.T) {
	m := newTestModel()
	m.footer = "pane splits saved"
	m.footerSeq = 4
	older, _ := m.Update(footerClearMsg{seq: 3})
	if got := older.(model).footer; got == "" {
		t.Fatal("older timer cleared newer footer")
	}
	current, _ := older.(model).Update(footerClearMsg{seq: 4})
	if got := current.(model).footer; got != "" {
		t.Fatalf("current timer left footer %q", got)
	}
}

func TestEmptyActionsRenderInPanelBottomBorders(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 100, 30
	m.cfg.Providers = nil
	if got := ansi.Strip(m.renderPane(panelStatus, 80, 12)); !strings.Contains(got, "add provider or integration") {
		t.Fatalf("status border missing action:\n%s", got)
	}
	m = newTestModel()
	m.cfg.Providers[0].Models = nil
	if got := ansi.Strip(m.renderPane(panelFavourites, 50, 10)); !strings.Contains(got, "set a favourite") {
		t.Fatalf("favourites border missing action:\n%s", got)
	}
	m.st = state.New()
	if got := ansi.Strip(m.renderPane(panelStats, 80, 12)); !strings.Contains(got, "check to measure") {
		t.Fatalf("stats border missing action:\n%s", got)
	}
}

func TestThemeNoticeExpiresAndMenuCycleStaysOpen(t *testing.T) {
	m := newTestModel()
	m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: []menuItem{
		{"cycle theme", "main.theme"},
	}}
	next, cmd := m.menuKey("enter")
	got := next.(model)
	if got.ov.kind != overlayMenu {
		t.Fatal("theme cycle closed menu")
	}
	if got.footer == "" || cmd == nil {
		t.Fatalf("theme cycle did not schedule transient notice: footer=%q", got.footer)
	}
	cleared, _ := got.Update(cmd())
	if cleared.(model).footer != "" {
		t.Fatalf("theme notice persisted: %q", cleared.(model).footer)
	}
}
