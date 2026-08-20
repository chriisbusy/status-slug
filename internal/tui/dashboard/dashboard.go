// Package dashboard implements the bubbletea v2 three-pane status TUI.
package dashboard

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
	"github.com/chriisbusy/status-slug/internal/tui/wizard"
)

// panelID identifies a dashboard pane.
type panelID int

const (
	panelStatus panelID = iota
	panelUsage
	panelFavourites
	panelStats
	panelCount
)

var panelNames = [panelCount]string{"status", "usage", "favourites", "stats"}

// panelTitleSegs decomposes a panel heading into (before, key, after) so the
// bound key renders highlighted in place — btop's convention:
// [s]tatus, [u]sage, [f]avourites, s[t]ats.
func panelTitleSegs(p panelID) (before, key, after string) {
	switch p {
	case panelStatus:
		return "", "s", "tatus"
	case panelUsage:
		return "", "u", "sage"
	case panelFavourites:
		return "", "f", "avourites"
	case panelStats:
		return "s", "t", "ats"
	}
	return "", "", ""
}

// panelChrome returns the section color used for each pane's border and title.
// This matches btop's theme language: each panel owns a consistent section
// color, while ok/warn/err remain semantic inside rows.
func (m model) panelChrome(p panelID) string {
	role := theme.PaneStatus
	switch p {
	case panelUsage:
		role = theme.PaneUsage
	case panelFavourites:
		role = theme.PaneFavourites
	case panelStats:
		role = theme.PaneStats
	}
	if c := m.palette[role]; c != "" {
		return c
	}
	return m.palette[theme.Accent]
}

// styledTitle renders a panel heading using that panel's chrome color.
func (m model) styledTitle(p panelID, extra string) string {
	before, key, after := panelTitleSegs(p)
	chrome := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(p))).Bold(true)
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(p)))
	out := title.Render(before) + chrome.Render("["+key+"]") + title.Render(after)
	if extra != "" {
		out += title.Render(extra)
	}
	return out
}

// Messages.
type checkResultMsg struct {
	provider string
	modelID  string
	result   check.Result
}
type autoUsageMsg struct {
	updates []provider.MeterUpdate
}
type tickMsg time.Time

// model is the root bubbletea model.
type model struct {
	cfg        config.Config
	st         *state.File
	palette    theme.Palette
	themeWarns []theme.Warning

	width, height int
	focused       panelID
	zoomed        bool

	// Per-pane scroll offsets and selections.
	sel    [panelCount]int
	scroll [panelCount]int

	// Check-in-flight state.
	checking     bool
	pendingCount int
	spin         spinner.Model

	// Overlay state machine (see overlays.go).
	ov overlayState

	// Wizard popup (modal). Non-nil while the setup wizard is open.
	wiz *wizard.Model

	// Footer one-shot message (warnings, confirmations).
	footer string

	// Mouse hit regions, recomputed each render.
	regions []hitRegion

	// Panel prefs persisted in state (sort, group).
	prefs panelPrefs

	// Animated meter bars, keyed "<provider>/<meter>".
	bars   map[string]progress.Model
	barPct map[string]float64

	// lastCheck for auto-refresh bookkeeping.
	lastCheck time.Time

	// Resize debounce state.
	resizeSeq          int
	pendingW, pendingH int
}

// hitRegion is a clickable rectangle.
type hitRegion struct {
	kind       string // "heading", "row", "check-button"
	panel      panelID
	row        int // for "row"
	x, y, w, h int
}

// panelPrefs holds per-panel UI prefs persisted in state.json ui.panels.
type panelPrefs struct {
	statusSort     string // "name"|"status"|"latency"|"checked"
	statusGroup    bool
	favSort        string
	statsSort      string // column key
	statsSortDir   int    // 0=default, 1=asc, 2=desc
	statsShowFavs  bool
	usageSortAlpha bool
}

// Run starts the dashboard TUI (loads config+state itself).
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	return RunWith(cfg, st)
}

// RunWith starts the dashboard with the given config and state.
// With no providers configured, the setup wizard opens as a popup.
func RunWith(cfg config.Config, st *state.File) error {
	p := tea.NewProgram(New(cfg, st))
	_, err := p.Run()
	return err
}

// RunWizard starts the dashboard with the setup wizard popup already open
// (used by `sslug setup`).
func RunWizard(cfg config.Config, st *state.File, reconfigure string) error {
	m := New(cfg, st)
	m.openWizard(reconfigure)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// openWizard mounts the setup wizard as a modal popup.
func (m *model) openWizard(reconfigure string) {
	w := wizard.New(m.cfg, reconfigure)
	m.wiz = &w
}

// closeWizard handles a finished/aborted wizard popup.
func (m *model) closeWizard() {
	if m.wiz == nil {
		return
	}
	if m.wiz.IsDone() {
		m.cfg = m.wiz.Config()
		// Reload palette in case nothing else changed; cheap and consistent.
		pal, warns := theme.LoadFromSettings(m.cfg.Settings)
		m.palette = pal
		if len(warns) > 0 {
			m.footer = warns[0].Message
		} else {
			m.footer = "provider saved"
		}
	} else if err := m.wiz.Err(); err != nil {
		m.footer = "setup: " + err.Error()
	} else {
		m.footer = "setup aborted"
	}
	m.wiz = nil
}

// New builds the root model (exported for tests).
func New(cfg config.Config, st *state.File) model {
	palette, warns := theme.LoadFromSettings(cfg.Settings)
	m := model{
		cfg:        cfg,
		st:         st,
		palette:    palette,
		themeWarns: warns,
		focused:    panelStatus,
	}
	m.spin = spinner.New(spinner.WithSpinner(spinner.Line))
	m.prefs = loadPrefs(st)
	if len(warns) > 0 {
		m.footer = warns[0].Message
	}
	if m.activeViewDef().Name == "full" && st.UI.View != "" && st.UI.View != "full" &&
		m.viewByName(st.UI.View) == nil {
		m.footer = fmt.Sprintf("unknown view %q — using full", st.UI.View)
	}
	// First run: the setup wizard opens as a popup over the dashboard.
	if len(cfg.Providers) == 0 {
		m.openWizard("")
	}
	_ = m.syncBars() // establish bar models at construction
	return m
}

// loadPrefs reads persisted panel prefs from state.
func loadPrefs(st *state.File) panelPrefs {
	p := panelPrefs{statusSort: "name", favSort: "name", statsSort: "", statsShowFavs: true}
	get := func(k string) string {
		if st.UI.Panels == nil {
			return ""
		}
		return st.UI.Panels[k]
	}
	if v := get("status.sort"); v != "" {
		p.statusSort = v
	}
	p.statusGroup = get("status.group") == "1"
	if v := get("favourites.sort"); v != "" {
		p.favSort = v
	}
	if v := get("stats.sort"); v != "" {
		p.statsSort = v
	}
	if get("stats.favs") == "0" {
		p.statsShowFavs = false
	}
	p.statsSortDir = 0
	if get("stats.dir") == "asc" {
		p.statsSortDir = 1
	} else if get("stats.dir") == "desc" {
		p.statsSortDir = 2
	}
	return p
}

// savePrefs persists panel prefs to state.
func (m *model) savePrefs() {
	if m.st.UI.Panels == nil {
		m.st.UI.Panels = map[string]string{}
	}
	u := m.st.UI.Panels
	u["status.sort"] = m.prefs.statusSort
	if m.prefs.statusGroup {
		u["status.group"] = "1"
	} else {
		u["status.group"] = "0"
	}
	u["favourites.sort"] = m.prefs.favSort
	u["stats.sort"] = m.prefs.statsSort
	u["stats.dir"] = []string{"", "asc", "desc"}[m.prefs.statsSortDir]
	if m.prefs.statsShowFavs {
		u["stats.favs"] = "1"
	} else {
		u["stats.favs"] = "0"
	}
	_ = m.st.Save()
}

// Init implements tea.Model.
func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.wiz != nil {
		// Wizard opened at construction (first run): start its form
		// lifecycle (cursor blink, field focus) too.
		cmds = append(cmds, m.wiz.Init())
	}
	if m.cfg.Settings.CheckOnLaunch {
		cmds = append(cmds, func() tea.Msg { return checkNowMsg{} })
	}
	if m.cfg.Settings.AutoRefresh > 0 {
		cmds = append(cmds, m.tickCmd())
	}
	return tea.Batch(cmds...)
}

func (m model) tickCmd() tea.Cmd {
	d := time.Duration(m.cfg.Settings.AutoRefresh) * time.Second
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// resizeApplyMsg fires after a resize debounce window; the latest seq wins.
type resizeApplyMsg struct{ seq int }

// resizeDebounce coalesces pane-resize drag storms into one reflow.
const resizeDebounce = 80 * time.Millisecond

// Update implements tea.Model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Resize: debounce — repaint once after the drag settles, not on every
	// intermediate size (the flicker the operator reported).
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.pendingW, m.pendingH = ws.Width, ws.Height
		m.resizeSeq++
		seq := m.resizeSeq
		return m, tea.Tick(resizeDebounce, func(time.Time) tea.Msg {
			return resizeApplyMsg{seq}
		})
	}
	if ra, ok := msg.(resizeApplyMsg); ok {
		if ra.seq != m.resizeSeq {
			return m, nil // a newer size already superseded this one
		}
		m.width, m.height = m.pendingW, m.pendingH
		m.bars = nil // bar models carry width; recreate at the new size
		if m.wiz != nil {
			wm, cmd := m.wiz.UpdateModel(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			m.wiz = &wm
			return m, cmd
		}
		return m, nil
	}

	// Wizard popup is modal: it gets every other message first.
	if m.wiz != nil {
		wm, cmd := m.wiz.UpdateModel(msg)
		m.wiz = &wm
		if wm.IsDone() || wm.IsAborted() {
			m.closeWizard()
			return m, nil
		}
		return m, cmd
	}

	switch msg := msg.(type) {

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		return m.handleClick(msg.Mouse())
	case tea.MouseWheelMsg:
		return m.handleWheel(msg.Mouse())

	case checkResultMsg:
		return m.handleCheckResult(msg)
	case autoUsageMsg:
		return m.handleAutoUsage(msg)

	case checkNowMsg:
		cmd := m.startCheckAll()
		return m, cmd

	case tickMsg:
		if m.cfg.Settings.AutoRefresh > 0 && !m.checking {
			cmd := m.startCheckAll()
			return m, tea.Batch(cmd, m.tickCmd())
		}
		return m, m.tickCmd()

	case spinner.TickMsg:
		if m.checking {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
		return m, nil

	case progress.FrameMsg:
		// Animate meter bars toward their targets.
		var cmds []tea.Cmd
		for k, bar := range m.bars {
			nb, cmd := bar.Update(msg)
			m.bars[k] = nb
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil
	}
	// Overlay components receive messages even when not key/mouse (e.g. huh).
	if m.ov.kind != overlayNone {
		return m.forwardToOverlay(msg)
	}
	return m, nil
}

// --- input handling ---

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Overlay consumes keys first.
	if m.ov.kind != overlayNone {
		return m.overlayKey(msg)
	}

	key := msg.String()
	switch key {
	case "q", "ctrl+c":
		if m.cfg.Settings.ConfirmQuit || m.checking {
			m.ov = overlayState{kind: overlayConfirm, action: "quit",
				title: "Quit sslug?", body: "a check is in flight — quit anyway?"}
			return m, nil
		}
		return m, tea.Quit

	case "tab", "l":
		m.focused = m.nextVisiblePanel(1)
		return m, nil
	case "shift+tab", "h":
		m.focused = m.nextVisiblePanel(-1)
		return m, nil

	case "j", "down":
		m.moveSelection(1)
	case "k", "up":
		m.moveSelection(-1)
	case "pgdown":
		m.moveSelection(m.pageSize())
	case "pgup":
		m.moveSelection(-m.pageSize())

	case "c":
		if !m.checking {
			cmd := m.startCheckAll()
			return m, cmd
		}
	case "enter":
		if m.focused == panelStatus && !m.checking {
			if p := m.selectedProvider(); p != nil {
				cmd := m.startCheckOne(*p)
				return m, cmd
			}
		}
		if m.focused == panelFavourites && !m.checking {
			if pv, mod := m.selectedFavourite(); mod != nil {
				cmd := m.startCheckModel(*pv, *mod)
				return m, cmd
			}
		}
		if !m.checking {
			cmd := m.startCheckAll()
			return m, cmd
		}

	case "z":
		m.zoomed = !m.zoomed
	case "?":
		m.ov = m.newHelpOverlay()
	case "i":
		m.ov = m.newInspectOverlay()
	case "g":
		m.ov = m.newIntegrationsOverlay()
	case "m":
		m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: []menuItem{
			{"add provider", "main.add"},
			{"settings", "main.settings"},
			{"cycle theme", "main.theme"},
			{"cycle view", "main.view"},
			{"integrations", "main.integrations"},
			{"help", "main.help"},
			{"quit", "main.quit"},
		}}
		return m, nil
	case "s":
		m.ov = m.newMenuOverlay(panelStatus)
	case "u":
		m.ov = m.newMenuOverlay(panelUsage)
	case "f":
		m.ov = m.newMenuOverlay(panelFavourites)
	case "t":
		m.ov = m.newMenuOverlay(panelStats)
	case "p":
		return m.cycleView(), nil
	case "e":
		return m.cycleTheme(), nil
	case "o":
		m.ov = m.newSettingsOverlay()
		if m.ov.kind == overlayForm {
			return m, dashBlinkTick()
		}
	case "a":
		m.openWizard("")
		if m.wiz != nil {
			return m, m.wiz.Init()
		}
		return m, nil
	case "r":
		// Reconfigure the selected provider through the wizard popup.
		if m.focused == panelStatus {
			if p := m.selectedProvider(); p != nil {
				m.openWizard(p.Name)
				if m.wiz != nil {
					return m, m.wiz.Init()
				}
			}
		}
		return m, nil
	case "d":
		if m.focused == panelStatus {
			if p := m.selectedProvider(); p != nil {
				m.ov = overlayState{kind: overlayConfirm, action: "remove:" + p.Name,
					title: fmt.Sprintf("Remove provider %q?", p.Name),
					body:  "Its stored key will be deleted too."}
			}
		}
	}
	return m, nil
}

// nextVisiblePanel cycles focus across panels present in the active view.
func (m model) nextVisiblePanel(dir int) panelID {
	visible := m.visiblePanels()
	if len(visible) == 0 {
		return panelStatus
	}
	idx := 0
	for i, p := range visible {
		if p == m.focused {
			idx = i
		}
	}
	return visible[(idx+dir+len(visible))%len(visible)]
}

// visiblePanels returns panels of the active view in order.
func (m model) visiblePanels() []panelID {
	v := m.activeViewDef()
	var out []panelID
	for _, name := range v.Panels {
		if p := panelByName(name); p >= 0 {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = []panelID{panelStatus, panelUsage, panelFavourites, panelStats}
	}
	return out
}

func panelByName(name string) panelID {
	for i, n := range panelNames {
		if n == name {
			return panelID(i)
		}
	}
	return -1
}

// moveSelection moves selection in the focused pane, clamping.
func (m *model) moveSelection(delta int) {
	maxIdx := m.maxSelection(m.focused)
	if maxIdx < 0 {
		return
	}
	m.sel[m.focused] += delta
	if m.sel[m.focused] < 0 {
		m.sel[m.focused] = 0
	}
	if m.sel[m.focused] > maxIdx {
		m.sel[m.focused] = maxIdx
	}
	vis := m.paneContentHeight()
	if vis <= 0 {
		return
	}
	if m.sel[m.focused] < m.scroll[m.focused] {
		m.scroll[m.focused] = m.sel[m.focused]
	}
	if m.sel[m.focused] >= m.scroll[m.focused]+vis {
		m.scroll[m.focused] = m.sel[m.focused] - vis + 1
	}
}

func (m model) maxSelection(p panelID) int {
	switch p {
	case panelStatus:
		return len(m.sortedProviders()) - 1
	case panelFavourites:
		return len(m.favouriteList()) - 1
	case panelUsage:
		return len(m.usageEntries()) - 1
	case panelStats:
		return len(m.statsRows()) - 1
	}
	return 0
}

// pageSize for PgUp/PgDn.
func (m model) pageSize() int {
	if h := m.paneContentHeight(); h > 2 {
		return h - 1
	}
	return 5
}

// headerLines is the dashboard header height in rows.
const headerLines = 2

// paneContentHeight is the inner height of a normal (non-zoomed) pane.
func (m model) paneContentHeight() int {
	h := (m.height-headerLines-1)/2 - 2
	if h < 1 {
		return 1
	}
	return h
}

// --- checking ---

// checkNowMsg triggers a check-all from Init (which cannot mutate the model).
type checkNowMsg struct{}

func (m *model) startCheckAll() tea.Cmd {
	return m.startChecks("")
}

func (m *model) startCheckOne(p config.Provider) tea.Cmd {
	return m.startChecks(p.Name)
}

func (m *model) startCheckModel(p config.Provider, mod config.Model) tea.Cmd {
	if m.checking {
		return nil
	}
	m.checking = true
	m.pendingCount = 1
	timeout := m.probeTimeout()
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		key, _ := secret.Resolve(p.KeyRef)
		res := provider.New(p.Kind).ProbeModel(ctx, check.NewDoer(timeout, key), p, mod.ID)
		return checkResultMsg{provider: p.Name, modelID: mod.ID, result: res}
	})
}

func cloneAutoMeterConfig(cfg config.Config) config.Config {
	out := config.Config{Settings: cfg.Settings}
	out.Providers = make([]config.Provider, len(cfg.Providers))
	for i, p := range cfg.Providers {
		out.Providers[i] = p
		out.Providers[i].Meters = append([]config.Meter(nil), p.Meters...)
	}
	return out
}

// startChecks probes all enabled providers + favourites, or one provider if
// only is non-empty. Pointer receiver: sets checking/pendingCount on the
// real model so the spinner, quit guard, and re-check guard all work.
func (m *model) startChecks(only string) tea.Cmd {
	if m.checking {
		return nil
	}
	m.checking = true

	timeout := m.probeTimeout()
	autoCfg := cloneAutoMeterConfig(m.cfg)
	jobs := provider.BuildJobs(m.cfg, func(p config.Provider) string {
		key, _ := secret.Resolve(p.KeyRef)
		return key
	}, timeout, only)

	if len(jobs) == 0 {
		m.checking = false
		return nil
	}
	m.pendingCount = len(jobs)

	cmds := make([]tea.Cmd, len(jobs))
	for i, j := range jobs {
		j := j
		cmds[i] = func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			res := j.Run(ctx)
			return checkResultMsg{provider: j.Provider, modelID: j.ModelID, result: res}
		}
	}
	// Bound concurrency.
	if len(cmds) > check.PoolSize {
		var batched []tea.Cmd
		for i := 0; i < len(cmds); i += check.PoolSize {
			end := i + check.PoolSize
			if end > len(cmds) {
				end = len(cmds)
			}
			batched = append(batched, tea.Batch(cmds[i:end]...))
		}
		cmds = batched
	}
	cmds = append(cmds, m.spin.Tick, func() tea.Msg {
		updates := provider.RefreshAutoMeters(context.Background(), autoCfg, timeout, only, func(p config.Provider) string {
			key, _ := secret.Resolve(p.KeyRef)
			return key
		})
		return autoUsageMsg{updates: updates}
	})
	return tea.Batch(cmds...)
}

// prevStatus returns the provider's last recorded status, or "unknown".
func prevStatus(st *state.File, provider string) string {
	if ps := st.Providers[provider]; ps != nil && ps.LastCheck != nil {
		return ps.LastCheck.Status
	}
	return "unknown"
}

// shouldBell reports whether a status transition rings the alert bell:
// only transitions INTO down from a non-down state.
func shouldBell(prev, next string) bool {
	return prev != "down" && next == "down"
}

// syncBars animates meter bars toward their current values. Called after
// any state change that can move a meter (check results, usage set).
func (m *model) syncBars() tea.Cmd {
	if m.bars == nil {
		m.bars = map[string]progress.Model{}
		m.barPct = map[string]float64{}
	}
	w := m.width/3 - 8
	if w < 10 {
		w = 10
	}
	if w > 40 {
		w = 40
	}
	var cmds []tea.Cmd
	for _, p := range m.cfg.Providers {
		for _, meter := range p.Meters {
			if meter.Cap <= 0 {
				continue
			}
			key := p.Name + "/" + meter.Name
			val := meter.Used
			if mv := m.st.GetMeter(p.Name, meter.Name); mv != nil {
				val = mv.Value
			}
			pct := val / meter.Cap
			if pct > 1 {
				pct = 1
			}
			bar, ok := m.bars[key]
			if !ok {
				bar = progress.New(
					progress.WithWidth(w),
					progress.WithoutPercentage(),
					progress.WithColorFunc(func(total, current float64) color.Color {
						ratio := current / total
						switch {
						case ratio >= 0.85:
							return lipgloss.Color(m.palette[theme.Err])
						case ratio >= 0.6:
							return lipgloss.Color(m.palette[theme.Warn])
						default:
							return lipgloss.Color(m.palette[theme.OK])
						}
					}),
				)
				m.bars[key] = bar
			}
			if m.barPct[key] != pct {
				m.barPct[key] = pct
				if cmd := bar.SetPercent(pct); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

func (m model) probeTimeout() time.Duration {
	if m.cfg.Settings.ProbeTimeout > 0 {
		return time.Duration(m.cfg.Settings.ProbeTimeout) * time.Second
	}
	return 10 * time.Second
}

func (m model) handleCheckResult(msg checkResultMsg) (tea.Model, tea.Cmd) {
	scr := state.CheckResult{
		Status:    string(msg.result.Status),
		Reason:    msg.result.Reason,
		HTTPCode:  msg.result.HTTPCode,
		LatencyMs: msg.result.LatencyMs,
		CheckedAt: msg.result.CheckedAt,
	}
	if msg.modelID == "" && m.cfg.Settings.AlertBell && shouldBell(prevStatus(m.st, msg.provider), scr.Status) {
		fmt.Print("\a")
	}
	if msg.modelID == "" {
		m.st.RecordCheck(msg.provider, scr, m.cfg.Settings.HistoryLength)
	} else {
		m.st.RecordModelCheck(msg.provider, msg.modelID, scr, m.cfg.Settings.HistoryLength)
	}
	_ = m.st.Save()
	m.pendingCount--
	if m.pendingCount <= 0 {
		m.pendingCount = 0
		m.checking = false
		m.lastCheck = time.Now()
	}
	return m, m.syncBars()
}

func (m model) handleAutoUsage(msg autoUsageMsg) (tea.Model, tea.Cmd) {
	for _, u := range msg.updates {
		m.st.SetMeter(u.Provider, u.Meter, u.Value)
	}
	if len(msg.updates) > 0 {
		_ = m.st.Save()
	}
	return m, m.syncBars()
}

// --- views ---

func (m model) cycleView() model {
	views := m.viewCycleOrder()
	current := m.st.UI.View
	if current == "" {
		current = "full"
	}
	next := views[0]
	for i, v := range views {
		if v == current {
			next = views[(i+1)%len(views)]
			break
		}
	}
	m.st.UI.View = next
	_ = m.st.Save()
	// Clamp scroll/selection so no pane loses its content to out-of-range
	// offsets in the new layout.
	for p := panelID(0); p < panelCount; p++ {
		if max := m.maxSelection(p); m.sel[p] > max && max >= 0 {
			m.sel[p] = max
		}
		if m.sel[p] < 0 {
			m.sel[p] = 0
		}
		if max := m.maxScroll(p); m.scroll[p] > max {
			m.scroll[p] = max
		}
		if m.scroll[p] < 0 {
			m.scroll[p] = 0
		}
	}
	return m
}

// viewCycleOrder returns builtin names then user view names, deduped.
func (m model) viewCycleOrder() []string {
	names := []string{"full", "compact", "status-only", "stats-only"}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	for _, v := range m.cfg.Views {
		if !seen[v.Name] {
			seen[v.Name] = true
			names = append(names, v.Name)
		}
	}
	return names
}

func (m model) viewByName(name string) *config.View {
	// User views replace builtins by name (plan: "extend/replace by name").
	for i := range m.cfg.Views {
		if m.cfg.Views[i].Name == name {
			return &m.cfg.Views[i]
		}
	}
	for _, v := range builtinViews() {
		if v.Name == name {
			return &v
		}
	}
	return nil
}

// cycleTheme advances through builtin then user themes, live-swapping the
// palette and persisting the choice to config.
func (m model) cycleTheme() model {
	names := theme.BuiltinNames()
	if entries, err := os.ReadDir(config.ThemesDir()); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".theme") {
				names = append(names, strings.TrimSuffix(e.Name(), ".theme"))
			}
		}
	}
	cur := m.cfg.Settings.Theme
	if cur == "" {
		cur = "sstop"
	}
	next := names[0]
	for i, n := range names {
		if n == cur {
			next = names[(i+1)%len(names)]
			break
		}
	}
	m.cfg.Settings.Theme = next
	if err := config.Save(m.cfg); err != nil {
		m.footer = "save config: " + err.Error()
		return m
	}
	pal, warns := theme.LoadFromSettings(m.cfg.Settings)
	m.palette = pal
	if len(warns) > 0 {
		m.footer = warns[0].Message
	} else {
		m.footer = "theme: " + next
	}
	return m
}

// activeViewDef resolves the active view, falling back to full.
func (m model) activeViewDef() config.View {
	name := m.st.UI.View
	if name == "" {
		name = "full"
	}
	if v := m.viewByName(name); v != nil {
		return *v
	}
	return builtinViews()[0]
}

func builtinViews() []config.View {
	return []config.View{
		{Name: "full", Panels: []string{"status", "usage", "favourites", "stats"}, Arrangement: "grid", MainSplit: 0.66},
		{Name: "compact", Panels: []string{"status", "usage", "favourites", "stats"}, Arrangement: "stack", Compact: true, MainSplit: 0.66},
		{Name: "status-only", Panels: []string{"status"}, Arrangement: "stack", MainSplit: 1.0},
		{Name: "stats-only", Panels: []string{"stats"}, Arrangement: "stack", MainSplit: 1.0},
	}
}

// --- selections ---

func (m model) selectedProvider() *config.Provider {
	provs := m.sortedProviders()
	if m.sel[panelStatus] < len(provs) {
		return provs[m.sel[panelStatus]]
	}
	return nil
}

func (m model) selectedFavourite() (*config.Provider, *config.Model) {
	favs := m.favouriteList()
	if m.sel[panelFavourites] < len(favs) {
		f := favs[m.sel[panelFavourites]]
		return f.provider, f.model
	}
	return nil, nil
}

// favouriteEntry pairs a provider with one favourite model.
type favouriteEntry struct {
	provider *config.Provider
	model    *config.Model
}

func (m model) favouriteList() []favouriteEntry {
	var out []favouriteEntry
	for i := range m.cfg.Providers {
		p := &m.cfg.Providers[i]
		for j := range p.Models {
			if p.Models[j].Favourite {
				out = append(out, favouriteEntry{p, &p.Models[j]})
			}
		}
	}
	// Sort by preference.
	switch m.prefs.favSort {
	case "latency":
		sortFavs(out, func(f favouriteEntry) float64 {
			if ps := m.st.Providers[f.provider.Name]; ps != nil {
				if ms := ps.Models[f.model.ID]; ms != nil && ms.LastCheck != nil {
					return ms.LastCheck.LatencyMs
				}
			}
			return 1e12
		})
	case "status":
		sortFavs(out, func(f favouriteEntry) float64 {
			if ps := m.st.Providers[f.provider.Name]; ps != nil {
				if ms := ps.Models[f.model.ID]; ms != nil && ms.LastCheck != nil {
					return float64(statusRank(ms.LastCheck.Status))
				}
			}
			return 3
		})
	}
	return out
}

func sortFavs(favs []favouriteEntry, key func(favouriteEntry) float64) {
	for i := 1; i < len(favs); i++ {
		for j := i; j > 0 && key(favs[j]) < key(favs[j-1]); j-- {
			favs[j], favs[j-1] = favs[j-1], favs[j]
		}
	}
}

func statusRank(s string) int {
	switch s {
	case "ok":
		return 0
	case "account":
		return 1
	case "down":
		return 2
	}
	return 3
}

// sortedProviders returns providers ordered per prefs.statusSort.
func (m model) sortedProviders() []*config.Provider {
	var out []*config.Provider
	for i := range m.cfg.Providers {
		out = append(out, &m.cfg.Providers[i])
	}
	key := func(p *config.Provider) (string, float64) {
		ps := m.st.Providers[p.Name]
		switch m.prefs.statusSort {
		case "status":
			r := 3
			if ps != nil && ps.LastCheck != nil {
				r = statusRank(ps.LastCheck.Status)
			}
			return "", float64(r)
		case "latency":
			if ps != nil && ps.LastCheck != nil {
				return "", ps.LastCheck.LatencyMs
			}
			return "", 1e12
		case "checked":
			if ps != nil && ps.LastCheck != nil {
				return "", float64(-ps.LastCheck.CheckedAt.Unix())
			}
			return "", 1e12
		default: // name
			return strings.ToLower(p.Name), 0
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			sj, fj := key(out[j])
			sp, fp := key(out[j-1])
			if sj < sp || (sj == sp && fj < fp) {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out
}

// --- View ---

// View implements tea.Model.
func (m model) View() tea.View {
	if m.width == 0 {
		return tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion}
	}
	content := m.render()
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// render produces the full frame and refreshes hit regions.
func (m model) render() string {
	m.regions = nil
	view := m.activeViewDef()
	stack := m.width < 100 || m.height < 24 || view.Arrangement == "stack" || m.zoomed

	header := m.renderHeader()
	footer := m.renderFooter()

	var body string
	switch {
	case m.zoomed:
		body = m.renderPane(m.focused, headerLines, m.width, m.height-headerLines-1, view.Compact)
	case stack:
		body = m.renderStack(view)
	default:
		body = m.renderGrid(view)
	}

	frame := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.ov.kind != overlayNone {
		frame = m.renderOverlay(frame)
	}
	if m.wiz != nil {
		frame = m.renderWizardPopup(frame)
	}
	return frame
}

// renderWizardPopup centers the wizard over the dashboard frame, btop-modal
// style: accent border, drop-shadow row spacing, background visible around it.
func (m model) renderWizardPopup(base string) string {
	content := m.wiz.Content()
	// Bound the popup to the screen.
	maxW := m.width - 6
	if maxW > 84 {
		maxW = 84
	}
	if maxW < 40 {
		maxW = m.width - 2
	}
	// Trim content lines to width.
	var lines []string
	for _, l := range strings.Split(content, "\n") {
		if lipgloss.Width(l) > maxW {
			l = truncate(l, maxW)
		}
		lines = append(lines, l)
	}
	maxH := m.height - 4
	if len(lines) > maxH && maxH > 4 {
		lines = lines[:maxH]
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.palette[theme.BoxBorderFocus])).
		Padding(0, 2).
		Render(strings.Join(lines, "\n"))
	if m.palette[theme.Bg] != "" {
		box = lipgloss.NewStyle().
			Background(lipgloss.Color(m.palette[theme.Bg])).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(m.palette[theme.BoxBorderFocus])).
			Padding(0, 2).
			Render(strings.Join(lines, "\n"))
	}
	title := " setup"
	if sn := m.wiz.StepName(); sn != "" {
		title = " setup · " + sn
	}
	return compositeCentered(base, box, m.width, m.height, title+" ")
}

// compositeCentered overlays content (with optional title spliced into its
// top border) at the center of base, sized w×h. Fully ANSI-aware: all
// slicing happens on display cells via x/ansi, never on raw runes — styled
// sequences are never cut or misaligned.
func compositeCentered(base, content string, w, h int, title string) string {
	baseLines := strings.Split(base, "\n")
	for len(baseLines) < h {
		baseLines = append(baseLines, "")
	}
	ovLines := strings.Split(content, "\n")
	ovW := 0
	for _, l := range ovLines {
		if lw := ansi.StringWidth(l); lw > ovW {
			ovW = lw
		}
	}
	startY := (h - len(ovLines)) / 2
	if startY < 0 {
		startY = 0
	}
	startX := (w - ovW) / 2
	if startX < 0 {
		startX = 0
	}
	endX := startX + ovW

	for i, ol := range ovLines {
		y := startY + i
		if y >= len(baseLines) {
			break
		}
		bl := baseLines[y]
		blW := ansi.StringWidth(bl)
		left := ansi.Cut(bl, 0, min(startX, blW))
		if pad := startX - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		right := ""
		if blW > endX {
			right = ansi.Cut(bl, endX, blW)
		}
		baseLines[y] = left + ol + right
	}

	if title != "" && startY < len(baseLines) {
		bl := baseLines[startY]
		pos := startX + 3
		blW := ansi.StringWidth(bl)
		tW := ansi.StringWidth(title)
		if pos+tW <= blW {
			left := ansi.Cut(bl, 0, pos)
			right := ansi.Cut(bl, pos+tW, blW)
			baseLines[startY] = left + title + right
		}
	}
	return strings.Join(baseLines, "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m model) renderHeader() string {
	ok, account, down := 0, 0, 0
	for _, p := range m.cfg.Providers {
		if ps := m.st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
			switch ps.LastCheck.Status {
			case "ok":
				ok++
			case "account":
				account++
			case "down":
				down++
			}
		}
	}
	g := m.glyphs()
	dot := func(c theme.Role, s string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[c])).Render(s)
	}
	dots := dot(theme.OK, fmt.Sprintf("%s%d", g.ok, ok)) + " " +
		dot(theme.Warn, fmt.Sprintf("%s%d", g.account, account)) + " " +
		dot(theme.Err, fmt.Sprintf("%s%d", g.down, down))
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))

	// Navigation indicators, btop-style: accent-keyed, clickable buttons.
	type navBtn struct {
		text string
		kind string
	}
	var checkBtn navBtn
	if m.checking {
		checkBtn = navBtn{m.spin.View() + muted.Render(" checking"), "check-button"}
	} else {
		checkBtn = navBtn{accent.Render("[c]") + muted.Render("heck"), "check-button"}
	}
	nav := []navBtn{
		{accent.Render("[m]") + muted.Render("enu"), "menu"},
		{accent.Render("[p]") + muted.Render("reset: "+m.activeViewDef().Name), "cycle-view"},
		{muted.Render("th") + accent.Render("[e]") + muted.Render("me"), "theme"},
		{accent.Render("[g]") + muted.Render("ateways"), "integrations"},
		checkBtn,
		{accent.Render("[o]") + muted.Render("ptions"), "settings"},
		{accent.Render("[?]"), "help"},
	}
	info := " " + dots
	for _, b := range nav {
		info += "   " + b.text
	}
	age := ""
	if !m.lastCheck.IsZero() {
		age = muted.Render(" · " + state.RelAge(time.Since(m.lastCheck)))
	}
	info += age
	clock := muted.Render(time.Now().Format("15:04"))

	// Brand art (gradient sweep) on the left; live summary beside it.
	art := theme.Art(m.palette)
	artW := 0
	for _, l := range strings.Split(art, "\n") {
		if w := ansi.StringWidth(l); w > artW {
			artW = w
		}
	}
	artW += 2 // breathing room before the summary

	// At narrow widths drop the clock first, then let the summary truncate.
	clockStr := clock
	if artW+ansi.StringWidth(info)+ansi.StringWidth(clockStr) > m.width {
		clockStr = ""
	}
	pad := m.width - artW - ansi.StringWidth(info) - ansi.StringWidth(clockStr)
	if pad < 0 {
		pad = 0
	}
	line1 := info + strings.Repeat(" ", pad) + clockStr
	joined := lipgloss.JoinHorizontal(lipgloss.Top, art, line1)
	// Hard clamp: the header must never be wider than the terminal —
	// JoinVertical pads every line in the frame to the widest one.
	var out []string
	for _, l := range strings.Split(joined, "\n") {
		out = append(out, truncate(l, m.width))
	}
	joined = strings.Join(out, "\n")

	// Register nav click regions by cumulative width.
	x := artW + 1 + ansi.StringWidth(" "+dots)
	for _, b := range nav {
		x += 3
		m.regions = append(m.regions, hitRegion{kind: b.kind, x: x, y: 0, w: ansi.StringWidth(b.text), h: 1})
		x += ansi.StringWidth(b.text)
	}
	return joined
}

// dashKeyMap backs the adaptive footer hints.
type dashKeyMap struct{}

func (dashKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "check")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "check one")),
		key.NewBinding(key.WithKeys("s/u/f/t"), key.WithHelp("s/u/f/t", "menus")),
		key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "views")),
		key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "theme")),
		key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "settings")),
		key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "integrations")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "menu")),
		key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inspect")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "edit")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")),
		key.NewBinding(key.WithKeys("z"), key.WithHelp("z", "zoom")),
		key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}

func (k dashKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

func (m model) renderFooter() string {
	if m.footer != "" {
		return " " + truncate(
			lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Warn])).Render(m.footer),
			m.width-1)
	}
	h := help.New()
	h.SetWidth(m.width - 1)
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	h.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	// bubbles help does not hard-clamp to its width — clamp ourselves so the
	// footer can never push the frame's corners out.
	return " " + truncate(h.ShortHelpView(dashKeyMap{}.ShortHelp()), m.width-1)
}

// renderStack renders panels vertically in view order.
func (m model) renderStack(view config.View) string {
	panels := m.visiblePanels()
	avail := m.height - headerLines - 1
	if avail < len(panels)*3 {
		avail = len(panels) * 3
	}
	per := avail / len(panels)
	var parts []string
	y := headerLines
	for i, p := range panels {
		h := per
		if i == len(panels)-1 {
			h = avail - per*(len(panels)-1)
		}
		parts = append(parts, m.renderPane(p, y, m.width, h, view.Compact))
		y += h
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderGrid renders the 2-column grid: round-robin from view panel order.
func (m model) renderGrid(view config.View) string {
	panels := m.visiblePanels()
	var left, right []panelID
	for i, p := range panels {
		if i%2 == 0 {
			left = append(left, p)
		} else {
			right = append(right, p)
		}
	}
	if len(right) == 0 && len(left) > 1 {
		right = left[len(left)/2:]
		left = left[:len(left)/2]
	}
	if len(left) == 0 {
		left = right
		right = nil
	}

	split := view.MainSplit
	if split < 0.4 || split > 0.8 {
		split = 0.66
	}
	leftW := int(float64(m.width) * split)
	rightW := m.width - leftW
	avail := m.height - headerLines - 1

	renderCol := func(panels []panelID, w, y0 int) string {
		if len(panels) == 0 {
			return ""
		}
		per := avail / len(panels)
		var parts []string
		y := y0
		for i, p := range panels {
			h := per
			if i == len(panels)-1 {
				h = avail - per*(len(panels)-1)
			}
			parts = append(parts, m.renderPane(p, y, w, h, view.Compact))
			y += h
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}

	l := renderCol(left, leftW, headerLines)
	r := renderCol(right, rightW, headerLines)
	if r == "" {
		return l
	}
	// Register the left/right split for mouse: left column spans [0,leftW).
	m.regions = append(m.regions, hitRegion{kind: "column-split", panel: -1, x: leftW, y: headerLines, w: 0, h: avail})
	return lipgloss.JoinHorizontal(lipgloss.Top, l, r)
}

// renderPane renders one box. y0 is its absolute screen row (for hit regions).
func (m model) renderPane(p panelID, y0, w, h int, compact bool) string {
	title := m.styledTitle(p, "")
	if p == panelStatus {
		accent := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(p))).Bold(true)
		title += lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(p))).
			Render("  ") + accent.Render("[c]") +
			lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(p))).Render("heck all")
	}

	innerW := w - 2
	innerH := h - 2
	if innerW < 4 {
		innerW = 4
	}
	if innerH < 1 {
		innerH = 1
	}

	content := m.paneContent(p, innerW, innerH, compact)
	contentLines := strings.Split(content, "\n")
	for i, line := range contentLines {
		if lipgloss.Width(line) > innerW {
			contentLines[i] = truncate(line, innerW)
		}
	}
	for len(contentLines) < innerH {
		contentLines = append(contentLines, "")
	}
	if len(contentLines) > innerH {
		contentLines = contentLines[:innerH]
	}

	borderColor := m.panelChrome(p)
	bs := lipgloss.RoundedBorder()
	switch m.cfg.Settings.BorderStyle {
	case "square":
		bs = lipgloss.NormalBorder()
	case "thick":
		bs = lipgloss.ThickBorder()
	}

	// lipgloss v2 Width/Height include the border — w is the total.
	box := lipgloss.NewStyle().
		Border(bs).
		BorderForeground(lipgloss.Color(borderColor)).
		Width(w).
		Height(h).
		Render(strings.Join(contentLines, "\n"))

	// Replace the top border line with the title-embedded version, built
	// from separately-styled segments (never spliced into styled text).
	lines := strings.Split(box, "\n")
	if len(lines) > 0 {
		lines[0] = buildTitleBorder(w, title, borderColor, bs)
	}
	return strings.Join(lines, "\n")
}

// buildTitleBorder constructs the top border line as separately-styled
// segments: border in border color, title arriving pre-styled (bound key in
// accent per btop convention).
func buildTitleBorder(w int, styledTitle, color string, bs lipgloss.Border) string {
	if w < 4 {
		return ""
	}
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	titleTxt := " " + styledTitle + " "
	fill := w - 2 - ansi.StringWidth(titleTxt) - 1 // corners + leading dash
	if fill < 0 {
		titleTxt = ansi.Truncate(titleTxt, w-3, "")
		fill = 0
	}
	return borderStyle.Render(bs.TopLeft+bs.Top) +
		titleTxt +
		borderStyle.Render(strings.Repeat(bs.Top, fill)+bs.TopRight)
}

// truncate shortens s to w display cells without breaking ANSI sequences.
func truncate(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "")
}

// paneContent dispatches to the pane renderer.
func (m model) paneContent(p panelID, w, h int, compact bool) string {
	switch p {
	case panelStatus:
		return m.renderStatusPane(w, h, compact)
	case panelUsage:
		return m.renderUsagePane(w, h, compact)
	case panelFavourites:
		return m.renderFavouritesPane(w, h, compact)
	case panelStats:
		return m.renderStatsPane(w, h, compact)
	}
	return ""
}
