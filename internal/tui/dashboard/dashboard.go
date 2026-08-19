// Package dashboard implements the bubbletea v2 three-pane status TUI.
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
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

// panelBracket returns the heading marker per the plan's mockup:
// [s]tatus, [u]sage, [f]avourites, [t]stats — the menu key replaces the
// name's first letter when they match, prepends otherwise.
func panelBracket(p panelID) string {
	keys := [panelCount]string{"s", "u", "f", "t"}
	name := panelNames[p]
	k := keys[p]
	if strings.HasPrefix(name, k) {
		return "[" + k + "]" + name[len(k):]
	}
	return "[" + k + "]" + name
}

// Messages.
type checkResultMsg struct {
	provider string
	modelID  string
	result   check.Result
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
	// Field pointers for the active huh form (settings / meter forms).
	ovFields     map[string]*string
	ovBoolFields map[string]*bool

	// Footer one-shot message (warnings, confirmations).
	footer string

	// Mouse hit regions, recomputed each render.
	regions []hitRegion

	// Panel prefs persisted in state (sort, group).
	prefs panelPrefs

	// lastCheck for auto-refresh bookkeeping.
	lastCheck time.Time
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
func RunWith(cfg config.Config, st *state.File) error {
	p := tea.NewProgram(New(cfg, st))
	_, err := p.Run()
	return err
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

// Update implements tea.Model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		return m.handleClick(msg.Mouse())
	case tea.MouseWheelMsg:
		return m.handleWheel(msg.Mouse())

	case checkResultMsg:
		return m.handleCheckResult(msg)

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
	case "S":
		m.ov = m.newSettingsOverlay()
		if m.ov.kind == overlayForm {
			return m, m.ov.form.Init()
		}
	case "a":
		return m, tea.Quit // cmd layer runs the wizard after exit
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

// paneContentHeight is the inner height of a normal (non-zoomed) pane.
func (m model) paneContentHeight() int {
	h := (m.height-2)/2 - 2
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

// startChecks probes all enabled providers + favourites, or one provider if
// only is non-empty. Pointer receiver: sets checking/pendingCount on the
// real model so the spinner, quit guard, and re-check guard all work.
func (m *model) startChecks(only string) tea.Cmd {
	if m.checking {
		return nil
	}
	m.checking = true

	timeout := m.probeTimeout()
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
	cmds = append(cmds, m.spin.Tick)
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
		body = m.renderPane(m.focused, 0, m.width, m.height-2, view.Compact)
	case stack:
		body = m.renderStack(view)
	default:
		body = m.renderGrid(view)
	}

	frame := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.ov.kind != overlayNone {
		frame = m.renderOverlay(frame)
	}
	return frame
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
	dot := func(c theme.Role, s string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[c])).Render(s)
	}
	g := m.glyphs()
	dots := dot(theme.OK, fmt.Sprintf("%s%d", g.ok, ok)) + " " +
		dot(theme.Warn, fmt.Sprintf("%s%d", g.account, account)) + " " +
		dot(theme.Err, fmt.Sprintf("%s%d", g.down, down))
	preset := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).
		Render("[p]reset: ") + m.activeViewDef().Name
	clock := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted])).
		Render(time.Now().Format("15:04"))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true).
		Render("sslug")
	// Check button region.
	checkBtn := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).Render("[c]heck all")
	line := " " + title + "  " + dots + "   " + preset + "   " + checkBtn + strings.Repeat(" ", 2)
	// Right-align clock.
	pad := m.width - lipgloss.Width(line) - lipgloss.Width(clock)
	if pad < 1 {
		pad = 1
	}
	line += strings.Repeat(" ", pad) + clock
	// Register header hit regions.
	btnX := 1 + lipgloss.Width("sslug") + 2 + lipgloss.Width(fmt.Sprintf("●%d ◐%d ○%d", ok, account, down)) + 3 + lipgloss.Width("[p]reset: "+m.activeViewDef().Name) + 3
	m.regions = append(m.regions,
		hitRegion{kind: "cycle-view", x: btnX - lipgloss.Width("[p]reset: "+m.activeViewDef().Name) - 3, y: 0, w: lipgloss.Width("[p]reset: " + m.activeViewDef().Name), h: 1},
		hitRegion{kind: "check-button", x: btnX, y: 0, w: lipgloss.Width("[c]heck all"), h: 1},
	)
	return line
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
		key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "settings")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		key.NewBinding(key.WithKeys("z"), key.WithHelp("z", "zoom")),
		key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}

func (k dashKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

func (m model) renderFooter() string {
	if m.footer != "" {
		return " " + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Warn])).Render(m.footer)
	}
	h := help.New()
	h.SetWidth(m.width - 1)
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint]))
	h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	h.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	return " " + h.ShortHelpView(dashKeyMap{}.ShortHelp())
}

// renderStack renders panels vertically in view order.
func (m model) renderStack(view config.View) string {
	panels := m.visiblePanels()
	avail := m.height - 2
	if avail < len(panels)*3 {
		avail = len(panels) * 3
	}
	per := avail / len(panels)
	var parts []string
	y := 1 // after header
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
	avail := m.height - 2

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

	l := renderCol(left, leftW, 1)
	r := renderCol(right, rightW, 1)
	if r == "" {
		return l
	}
	// Register the left/right split for mouse: left column spans [0,leftW).
	m.regions = append(m.regions, hitRegion{kind: "column-split", panel: -1, x: leftW, y: 1, w: 0, h: avail})
	return lipgloss.JoinHorizontal(lipgloss.Top, l, r)
}

// renderPane renders one box. y0 is its absolute screen row (for hit regions).
func (m model) renderPane(p panelID, y0, w, h int, compact bool) string {
	focused := m.focused == p
	title := panelBracket(p)
	if p == panelStatus {
		title += "  [c]heck all"
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

	borderColor := m.palette[theme.BoxBorder]
	if focused {
		borderColor = m.palette[theme.BoxBorderFocus]
	}
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
		Width(innerW).
		Height(innerH).
		Render(strings.Join(contentLines, "\n"))

	// Embed the title into the top border line.
	lines := strings.Split(box, "\n")
	if len(lines) > 0 {
		lines[0] = embedTitle(lines[0], " "+title+" ", borderColor, m.palette)
	}
	return strings.Join(lines, "\n")
}

// embedTitle splices a title string into a border line at column 2.
func embedTitle(borderLine, title, color string, pal theme.Palette) string {
	runes := []rune(borderLine)
	tr := []rune(title)
	if len(runes) < len(tr)+3 {
		return borderLine
	}
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(title)
	prefix := string(runes[:2])
	suffix := string(runes[2+len(tr):])
	return prefix + styled + suffix
}

// truncate shortens s to w display cells.
func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	out := make([]rune, 0, w)
	cw := 0
	for _, r := range runes {
		rw := lipgloss.Width(string(r))
		if cw+rw > w {
			break
		}
		out = append(out, r)
		cw += rw
	}
	return string(out)
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
