// Package dashboard implements the bubbletea v2 three-pane status TUI.
package dashboard

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
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

// panelTitleSegs decomposes a panel heading so only its activation letter uses
// btop's action color while the title remains normal foreground.
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

// styledTitle renders a panel title with one action-colored activation letter.
func (m model) styledTitle(p panelID) string {
	before, key, after := panelTitleSegs(p)
	activation := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	return title.Render(before) + activation.Render(key) + title.Render(after)
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
type footerClearMsg struct{ seq int }

func footerClearCmd(seq int) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return footerClearMsg{seq: seq} })
}

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
	footer    string
	footerSeq int

	// Panel prefs persisted in state (sort, group).
	prefs panelPrefs

	// lastCheck for auto-refresh bookkeeping.
	lastCheck   time.Time
	moshiStatus *moshiLocalStatus

	// Resize debounce state.
	resizeSeq          int
	pendingW, pendingH int
	dragSplit          string
}

// hitRegion is a clickable rectangle.
type hitRegion struct {
	kind       string
	x, y, w, h int
}

type headerButton struct {
	text string
	kind string
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

func RunWith(cfg config.Config, st *state.File) error {
	defer resetBasicMouse()
	p := tea.NewProgram(New(cfg, st))
	_, err := p.Run()
	return err
}

func RunWizard(cfg config.Config, st *state.File, reconfigure string) error {
	defer resetBasicMouse()
	m := New(cfg, st)
	m.openWizard(reconfigure)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

func resetBasicMouse() {
	_, _ = fmt.Fprint(os.Stdout, ansi.ResetModeMouseNormal+ansi.ResetModeMouseExtSgr)
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
	var settingWarnings []string
	switch cfg.Settings.BorderStyle {
	case "", "rounded":
		cfg.Settings.BorderStyle = "rounded"
	case "square", "thick":
	default:
		settingWarnings = append(settingWarnings, fmt.Sprintf("unknown border style %q — using rounded", cfg.Settings.BorderStyle))
		cfg.Settings.BorderStyle = "rounded"
	}
	switch cfg.Settings.GraphStyle {
	case "", "tty":
		cfg.Settings.GraphStyle = "tty"
	case "block", "braille":
	default:
		settingWarnings = append(settingWarnings, fmt.Sprintf("unknown graph style %q — using tty", cfg.Settings.GraphStyle))
		cfg.Settings.GraphStyle = "tty"
	}
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
	for _, providerState := range st.Providers {
		if providerState != nil && providerState.LastCheck != nil &&
			providerState.LastCheck.CheckedAt.After(m.lastCheck) {
			m.lastCheck = providerState.LastCheck.CheckedAt
		}
	}
	if len(warns) > 0 {
		settingWarnings = append(settingWarnings, warns[0].Message)
	}
	if m.activeViewDef().Name == "full" && st.UI.View != "" && st.UI.View != "full" &&
		m.viewByName(st.UI.View) == nil {
		settingWarnings = append(settingWarnings, fmt.Sprintf("unknown view %q — using full", st.UI.View))
	}
	if len(settingWarnings) > 0 {
		m.footer = strings.Join(settingWarnings, " · ")
	}
	// First run: the setup wizard opens as a popup over the dashboard.
	if len(cfg.Providers) == 0 {
		m.openWizard("")
	}
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
	if value := get("stats.sort"); value != "" {
		switch value {
		case "ago":
			p.statsSort = "age"
		case "p50":
			p.statsSort = "p95"
		case "name", "provider", "kind", "status", "latency", "history", "p95", "age":
			p.statsSort = value
		default:
			p.statsSort = ""
		}
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

// saveDashboardConfig merges dashboard-owned settings into the latest file so
// a long-running UI cannot overwrite externally updated providers or meters.
func (m *model) saveDashboardConfig() error {
	latest, err := config.Load()
	if err != nil {
		return err
	}
	latest.Settings, latest.Views = m.cfg.Settings, m.cfg.Views
	if err := config.Save(latest); err != nil {
		return err
	}
	m.cfg = latest
	return nil
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
	cmds = append(cmds, moshiStatusCmd())
	if m.footer != "" {
		cmds = append(cmds, footerClearCmd(m.footerSeq))
	}
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
func (m model) Update(msg tea.Msg) (next tea.Model, cmd tea.Cmd) {
	previousFooter, previousSeq := m.footer, m.footerSeq
	defer func() {
		updated, ok := next.(model)
		if !ok || updated.footer == "" || updated.footer == previousFooter || updated.footerSeq != previousSeq {
			return
		}
		updated.footerSeq++
		next = updated
		cmd = tea.Batch(cmd, footerClearCmd(updated.footerSeq))
	}()
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
		if m.wiz != nil {
			wm, cmd := m.wiz.UpdateModel(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			m.wiz = &wm
			return m, cmd
		}
		return m, nil
	}
	if statusMessage, ok := msg.(moshiStatusMsg); ok {
		if m.moshiStatus != nil && statusMessage.status.CheckedAt.Before(m.moshiStatus.CheckedAt) {
			return m, nil
		}
		m.moshiStatus = &statusMessage.status
		if m.ov.kind == overlayViewport && m.ov.title == "integrations" {
			m.ov = m.newIntegrationsOverlayWith(&statusMessage.status)
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
	case tea.MouseMotionMsg:
		return m.handleMouseMotion(msg.Mouse())
	case tea.MouseReleaseMsg:
		return m.handleMouseRelease(msg.Mouse())
	case tea.MouseWheelMsg:
		return m.handleWheel(msg.Mouse())

	case checkResultMsg:
		return m.handleCheckResult(msg)
	case autoUsageMsg:
		return m.handleAutoUsage(msg)
	case moshiRepairMsg:
		if msg.err != "" {
			m.footer = msg.err
		} else {
			m.footer = "Moshi hooks repaired"
			m.ov = m.newIntegrationsOverlay()
		}
		m.footerSeq++
		return m, tea.Batch(footerClearCmd(m.footerSeq), moshiStatusCmd())
	case footerClearMsg:
		if msg.seq == m.footerSeq {
			m.footer = ""
		}
		return m, nil

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
	if m.footer != "" {
		m.footer = ""
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
		return m, moshiStatusCmd()
	case "m":
		m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: m.mainMenuItems()}
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
		next := m.cycleTheme()
		return next, footerClearCmd(next.footerSeq)
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
	case "R":
		return m, moshiStatusCmd()
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

// nextVisiblePanel cycles admitted panes, or every configured pane while the
// terminal can display only one pane.
func (m model) nextVisiblePanel(dir int) panelID {
	visible := m.admittedPanels()
	if len(visible) <= 1 {
		visible = m.visiblePanels()
	}
	if len(visible) == 0 {
		return panelStatus
	}
	idx := 0
	for i, panel := range visible {
		if panel == m.focused {
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
	maxIndex := m.maxSelection(m.focused)
	if maxIndex < 0 {
		return
	}
	m.sel[m.focused] += delta
	if m.sel[m.focused] < 0 {
		m.sel[m.focused] = 0
	}
	if m.sel[m.focused] > maxIndex {
		m.sel[m.focused] = maxIndex
	}
	visible := m.selectableViewportHeight(m.focused)
	if m.sel[m.focused] < m.scroll[m.focused] {
		m.scroll[m.focused] = m.sel[m.focused]
	}
	if m.sel[m.focused] >= m.scroll[m.focused]+visible {
		m.scroll[m.focused] = m.sel[m.focused] - visible + 1
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

// pageSize returns the focused pane's real selectable viewport.
func (m model) pageSize() int {
	return max(1, m.selectableViewportHeight(m.focused))
}

func (m model) selectableViewportHeight(panel panelID) int {
	height := m.paneContentHeightFor(panel)
	switch panel {
	case panelStatus:
		reserved := min(len(m.moshiDashboardLines(1)), max(0, height-1))
		height -= 1 + reserved
	case panelFavourites:
		height--
	case panelStats:
		height -= 2
	}
	return max(1, height)
}

// headerLines is the dashboard header height in rows.
const headerLines = 2

// paneContentHeight returns the focused pane's actual inner height.
func (m model) paneContentHeight() int {
	return m.paneContentHeightFor(m.focused)
}

func (m model) paneContentHeightFor(panel panelID) int {
	for _, rect := range m.paneLayout() {
		if rect.panel == panel {
			return max(1, rect.h-2)
		}
	}
	return 1
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
	return m, nil
}

func (m model) handleAutoUsage(msg autoUsageMsg) (tea.Model, tea.Cmd) {
	for _, u := range msg.updates {
		m.st.SetMeter(u.Provider, u.Meter, u.Value)
	}
	if len(msg.updates) > 0 {
		_ = m.st.Save()
	}
	return m, nil
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
	if err := m.saveDashboardConfig(); err != nil {
		m.footer = "save config: " + err.Error()
		m.footerSeq++
		return m
	}
	pal, warns := theme.LoadFromSettings(m.cfg.Settings)
	m.palette = pal
	if len(warns) > 0 {
		m.footer = warns[0].Message
	} else {
		m.footer = "theme: " + next
	}
	m.footerSeq++
	return m
}

// activeViewDef resolves and normalizes the active view, falling back to full.
func (m model) activeViewDef() config.View {
	name := m.st.UI.View
	if name == "" {
		name = "full"
	}
	if v := m.viewByName(name); v != nil {
		return config.NormalizeView(*v)
	}
	return config.NormalizeView(builtinViews()[0])
}

func builtinViews() []config.View {
	return []config.View{
		{Name: "full", Panels: []string{"status", "stats", "usage", "favourites"}, TopRatio: 0.33, LeftRatio: 0.37, UsageRatio: 0.46},
		{Name: "compact", Panels: []string{"status", "stats", "usage", "favourites"}, TopRatio: 0.40, LeftRatio: 0.50, UsageRatio: 0.50},
		{Name: "status-only", Panels: []string{"status"}, TopRatio: 0.33, LeftRatio: 0.37, UsageRatio: 0.46},
		{Name: "stats-only", Panels: []string{"stats"}, TopRatio: 0.33, LeftRatio: 0.37, UsageRatio: 0.46},
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
		return tea.View{Content: ansi.SetModeMouseNormal + ansi.SetModeMouseExtSgr, AltScreen: true, MouseMode: tea.MouseModeNone}
	}
	content := ansi.SetModeMouseNormal + ansi.SetModeMouseExtSgr + m.render()
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeNone
	return v
}

// render produces the full frame.
func (m model) render() string {
	header := m.renderHeader()
	body := m.renderLayout()
	footer := m.renderFooter()
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
	titleText := " setup"
	if sn := m.wiz.StepName(); sn != "" {
		titleText = " setup · " + sn
	}
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette[theme.Accent])).
		Bold(true).
		Render(titleText + " ")
	return compositeCentered(base, box, m.width, m.height, title)
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

func (m model) headerNav() []headerButton {
	action := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	checkText := action.Render("c") + title.Render("heck")
	if m.checking {
		checkText = m.spin.View() + title.Render(" checking")
	}
	return []headerButton{
		{action.Render("m") + title.Render("enu"), "menu"},
		{action.Render("p") + title.Render("resets"), "cycle-view"},
		{action.Render("g") + title.Render("ateways"), "integrations"},
		{checkText, "check-button"},
		{action.Render("?") + title.Render(" help"), "help"},
	}
}

func (m model) headerRows() ([]string, []hitRegion) {
	ok, account, down, unknown := m.healthCounts()
	meterCount, _, favouriteCount := m.meterCounts()
	glyphs := m.glyphs()
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	action := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.OK])).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Warn])).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Err])).Bold(true)
	unknownStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Unknown]))

	health := title.Render("fleet ") +
		okStyle.Render(fmt.Sprintf("%s%d", glyphs.ok, ok)) + muted.Render("  ") +
		warnStyle.Render(fmt.Sprintf("%s%d", glyphs.account, account)) + muted.Render("  ") +
		errStyle.Render(fmt.Sprintf("%s%d", glyphs.down, down))
	if unknown > 0 {
		health += muted.Render("  ") + unknownStyle.Render(fmt.Sprintf("%s%d", glyphs.unknown, unknown))
	}
	checked := "not checked"
	if !m.lastCheck.IsZero() {
		checked = "checked " + state.RelAge(time.Since(m.lastCheck))
	}
	context := title.Render(fmt.Sprintf("%d providers", len(m.cfg.Providers))) +
		muted.Render(fmt.Sprintf("  ·  %d meters  ·  %d favourites  ·  %s", meterCount, favouriteCount, checked))
	clock := title.Render(time.Now().Format("15:04:05"))

	artLines := strings.Split(theme.Art(m.palette), "\n")
	for len(artLines) < headerLines {
		artLines = append(artLines, "")
	}
	artWidth := 0
	for _, line := range artLines[:headerLines] {
		artWidth = max(artWidth, ansi.StringWidth(line))
	}
	artWidth += 2
	place := func(art, left, right string) string {
		art = fitCells(art, artWidth)
		available := max(0, m.width-artWidth)
		rightWidth := min(available, ansi.StringWidth(right))
		right = ansi.Truncate(right, rightWidth, "")
		leftWidth := max(0, available-rightWidth)
		if rightWidth > 0 && leftWidth > 0 {
			leftWidth--
		}
		left = ansi.Truncate(left, leftWidth, "")
		gap := max(0, available-ansi.StringWidth(left)-ansi.StringWidth(right))
		return fitCells(art+left+strings.Repeat(" ", gap)+right, m.width)
	}
	renderButtons := func(buttons []headerButton) (string, []int) {
		var text string
		var offsets []int
		for index, button := range buttons {
			if index > 0 {
				text += "  "
			}
			offsets = append(offsets, ansi.StringWidth(text))
			text += button.text
		}
		return text, offsets
	}

	var rows []string
	var regions []hitRegion
	if m.width < 110 {
		menu := m.headerNav()[:1]
		menuText, menuOffsets := renderButtons(menu)
		viewButton := headerButton{action.Render("p") + title.Render(" views"), "cycle-view"}
		viewText, viewOffsets := renderButtons([]headerButton{viewButton})
		bottomLeft := viewText + muted.Render("  ·  "+checked)
		rows = []string{
			place(artLines[0], health, menuText),
			place(artLines[1], bottomLeft, clock),
		}
		menuX := m.width - ansi.StringWidth(menuText)
		regions = append(regions, hitRegion{kind: menu[0].kind, x: menuX + menuOffsets[0], y: 0, w: ansi.StringWidth(menu[0].text), h: 1})
		regions = append(regions, hitRegion{kind: viewButton.kind, x: artWidth + viewOffsets[0], y: 1, w: ansi.StringWidth(viewButton.text), h: 1})
	} else {
		buttons := m.headerNav()
		actions, offsets := renderButtons(buttons)
		rows = []string{
			place(artLines[0], health, actions),
			place(artLines[1], context, clock),
		}
		actionX := m.width - ansi.StringWidth(actions)
		for index, button := range buttons {
			regions = append(regions, hitRegion{kind: button.kind, x: actionX + offsets[index], y: 0, w: ansi.StringWidth(button.text), h: 1})
		}
	}
	return rows, regions
}

func (m model) renderHeader() string {
	rows, _ := m.headerRows()
	return strings.Join(rows, "\n")
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
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	item := func(key, desc string) string {
		return accent.Render(key) + muted.Render(desc)
	}
	primary := []string{
		item("tab", " focus"),
		item("m", " menu"),
		item("p", " views"),
		item("?", " help"),
		item("q", " quit"),
	}
	return " " + fitActionLine(primary, m.width-1, muted.Render(" · "))
}

func fitActionLine(parts []string, width int, sep string) string {
	if width <= 0 {
		return ""
	}
	var line string
	for _, part := range parts {
		candidate := part
		if line != "" {
			candidate = line + sep + part
		}
		if ansi.StringWidth(candidate) <= width {
			line = candidate
		}
	}
	if line == "" && len(parts) > 0 {
		return truncate(parts[0], width)
	}
	return line
}

// renderLayout renders every admitted pane from the shared geometry plan.
func (m model) renderLayout() string {
	type renderedPane struct {
		rect  paneRect
		lines []string
	}
	rects := m.paneLayout()
	_, bodyHeight := m.bodySize()
	rendered := make([]renderedPane, 0, len(rects))
	for _, rect := range rects {
		rendered = append(rendered, renderedPane{
			rect:  rect,
			lines: strings.Split(m.renderPane(rect.panel, rect.w, rect.h), "\n"),
		})
	}
	rows := make([]string, bodyHeight)
	for y := range bodyHeight {
		var segments []renderedPane
		for _, pane := range rendered {
			if y >= pane.rect.y && y < pane.rect.y+pane.rect.h {
				segments = append(segments, pane)
			}
		}
		sort.Slice(segments, func(i, j int) bool { return segments[i].rect.x < segments[j].rect.x })
		var line string
		x := 0
		for _, segment := range segments {
			if segment.rect.x > x {
				line += strings.Repeat(" ", segment.rect.x-x)
			}
			localY := y - segment.rect.y
			paneLine := ""
			if localY < len(segment.lines) {
				paneLine = fitCells(segment.lines[localY], segment.rect.w)
			}
			line += paneLine
			x = segment.rect.x + segment.rect.w
		}
		rows[y] = fitCells(line, m.width)
	}
	return strings.Join(rows, "\n")
}

// renderPane renders one semantic btop-style pane.
func (m model) renderPane(p panelID, w, h int) string {
	title := m.styledTitle(p)
	innerW := max(4, w-2)
	innerH := max(1, h-2)
	compact := innerW < 70
	content := m.paneContent(p, innerW, innerH, compact)
	contentLines := strings.Split(content, "\n")
	for i, line := range contentLines {
		contentLines[i] = fitCells(line, innerW)
	}
	for len(contentLines) < innerH {
		contentLines = append(contentLines, strings.Repeat(" ", innerW))
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
	box := lipgloss.NewStyle().
		Border(bs).
		BorderForeground(lipgloss.Color(borderColor)).
		Width(w).
		Height(h).
		Render(strings.Join(contentLines, "\n"))
	lines := strings.Split(box, "\n")
	if len(lines) > 0 {
		lines[0] = buildTitleBorder(w, title, borderColor, bs)
		if hint := m.paneBottomHint(p); hint != "" && len(lines) > 1 {
			lines[min(len(lines)-1, h-1)] = buildBottomBorder(w, hint, borderColor, bs)
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) paneBottomHint(panel panelID) string {
	action := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	item := func(key, text string) string { return action.Render(key) + title.Render(" "+text) }
	scroll := action.Render("↑") + title.Render(" scroll ") + action.Render("↓")
	join := func(items ...string) string { return strings.Join(items, muted.Render(" · ")) }
	phrase := func(text string, keyIndex int) string {
		runes := []rune(text)
		return title.Render(string(runes[:keyIndex])) + action.Render(string(runes[keyIndex])) + title.Render(string(runes[keyIndex+1:]))
	}
	switch panel {
	case panelStatus:
		if len(m.cfg.Providers) == 0 {
			return phrase("add provider or integration", 0)
		}
		return join(item("a", "add"), item("c", "check"), item("R", "moshi refresh"), scroll, item("s", "menu"))
	case panelUsage:
		if len(m.cfg.Providers) == 0 {
			return phrase("add provider or integration", 0)
		}
		meters := 0
		for _, providerConfig := range m.cfg.Providers {
			meters += len(providerConfig.Meters)
		}
		if meters == 0 {
			return phrase("check to measure", 0)
		}
		return join(item("c", "refresh"), scroll, item("u", "menu"))
	case panelFavourites:
		if len(m.favouriteList()) == 0 {
			return phrase("set a favourite", 6)
		}
		return join(item("⏎", "check"), scroll, item("f", "menu"))
	case panelStats:
		checks := 0
		for _, providerState := range m.st.Providers {
			if providerState != nil {
				checks += providerState.Counters.Checks
			}
		}
		if checks == 0 {
			return phrase("check to measure", 0)
		}
		return join(item("c", "check"), scroll, item("t", "menu"))
	}
	return ""
}

func (m model) healthCounts() (ok, account, down, unknown int) {
	for _, p := range m.cfg.Providers {
		if ps := m.st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
			switch ps.LastCheck.Status {
			case "ok":
				ok++
			case "account":
				account++
			case "down":
				down++
			default:
				unknown++
			}
		} else {
			unknown++
		}
	}
	return ok, account, down, unknown
}

func (m model) meterCounts() (total, auto, fav int) {
	for _, p := range m.cfg.Providers {
		for _, meter := range p.Meters {
			total++
			if meter.Kind == "auto" {
				auto++
			}
		}
		for _, mod := range p.Models {
			if mod.Favourite {
				fav++
			}
		}
	}
	return total, auto, fav
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

func buildBottomBorder(width int, styledHint, color string, border lipgloss.Border) string {
	if width < 4 {
		return ""
	}
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	hint := " " + styledHint + " "
	fill := width - 3 - ansi.StringWidth(hint)
	if fill < 0 {
		hint = ansi.Truncate(hint, width-3, "")
		fill = 0
	}
	return borderStyle.Render(border.BottomLeft+strings.Repeat(border.Bottom, fill)) +
		hint + borderStyle.Render(border.Bottom+border.BottomRight)
}

// truncate shortens s to w display cells without breaking ANSI sequences.
func truncate(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "")
}
func fitCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "")
	if pad := width - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
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
