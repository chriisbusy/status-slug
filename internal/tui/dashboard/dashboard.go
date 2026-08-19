// Package dashboard implements the bubbletea v2 three-pane status TUI.
package dashboard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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

// overlayMode identifies which overlay is open.
type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayHelp
	overlayInspect
	overlaySettings
	overlayConfirmQuit
	overlayConfirmReset
	overlayStatusMenu
	overlayUsageMenu
	overlayFavMenu
	overlayStatsMenu
	overlaySetValue
	overlayAddFav
	overlayRemoveProvider
)

// checkResultMsg is sent when a single probe completes.
type checkResultMsg struct {
	provider string
	modelID  string // empty = provider-level
	result   check.Result
}

// tickMsg is the auto-refresh tick.
type tickMsg time.Time

// spinnerTickMsg drives the braille spinner animation.
type spinnerTickMsg time.Time

// model is the root bubbletea model.
type model struct {
	cfg        config.Config
	st         *state.File
	palette    theme.Palette
	themeWarns []theme.Warning

	width, height int
	focused       panelID
	zoomed        bool
	overlay       overlayMode
	overlayData   string // text for inspect/help overlays

	// Per-pane scroll offsets and selections.
	sel    [panelCount]int
	scroll [panelCount]int

	// Check-in-flight state.
	checking     bool
	pendingCount int
	spinnerIdx   int

	// Footer message (one-time warnings, errors).
	footer string

	// Settings form state (minimal inline, no huh for live overlay).
	settingsField int

	// Auto-refresh.
	lastCheck time.Time

	// Confirm quit.
	pendingQuit bool
}

// Run starts the dashboard TUI.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	palette, warns := theme.LoadFromSettings(cfg.Settings)
	m := model{
		cfg:        cfg,
		st:         st,
		palette:    palette,
		themeWarns: warns,
		focused:    panelStatus,
	}
	p := tea.NewProgram(m)
	_, err = p.Run()
	return err
}

// RunWithInput starts the dashboard with a custom input reader (for tests).
func RunWithInput(cfg config.Config, st *state.File) error {
	palette, warns := theme.LoadFromSettings(cfg.Settings)
	m := model{cfg: cfg, st: st, palette: palette, themeWarns: warns}
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// Init implements tea.Model.
func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.cfg.Settings.CheckOnLaunch {
		cmds = append(cmds, m.startCheckAll())
	}
	if m.cfg.Settings.AutoRefresh > 0 {
		cmds = append(cmds, m.tickCmd())
	}
	return tea.Batch(cmds...)
}

// tickCmd returns a command that fires after auto_refresh_seconds.
func (m model) tickCmd() tea.Cmd {
	d := time.Duration(m.cfg.Settings.AutoRefresh) * time.Second
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// spinnerCmd drives the braille spinner while checking.
func (m model) spinnerCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) })
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
		return m.handleMouse(msg)

	case checkResultMsg:
		return m.handleCheckResult(msg)

	case tickMsg:
		if m.cfg.Settings.AutoRefresh > 0 && !m.checking {
			return m, tea.Batch(m.startCheckAll(), m.tickCmd())
		}
		return m, m.tickCmd()

	case spinnerTickMsg:
		if m.checking {
			m.spinnerIdx++
			return m, m.spinnerCmd()
		}
		return m, nil
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Overlay handling first.
	if m.overlay != overlayNone {
		return m.handleOverlayKey(key)
	}

	switch key {
	case "q", "ctrl+c":
		if m.cfg.Settings.ConfirmQuit || m.checking {
			m.overlay = overlayConfirmQuit
			return m, nil
		}
		return m, tea.Quit

	case "tab", "l":
		m.focused = (m.focused + 1) % panelCount
		return m, nil
	case "shift+tab", "h":
		m.focused = (m.focused - 1 + panelCount) % panelCount
		return m, nil

	case "j", "down":
		m.moveSelection(1)
		return m, nil
	case "k", "up":
		m.moveSelection(-1)
		return m, nil
	case "pgdown":
		m.moveSelection(10)
		return m, nil
	case "pgup":
		m.moveSelection(-10)
		return m, nil

	case "c", "enter":
		if !m.checking {
			return m, m.startCheckAll()
		}
		return m, nil

	case "z":
		m.zoomed = !m.zoomed
		return m, nil

	case "?":
		m.overlay = overlayHelp
		m.overlayData = helpText
		return m, nil

	case "i":
		m.overlay = overlayInspect
		m.overlayData = m.inspectText()
		return m, nil

	case "s":
		m.overlay = overlayStatusMenu
		return m, nil
	case "u":
		m.overlay = overlayUsageMenu
		return m, nil
	case "f":
		m.overlay = overlayFavMenu
		return m, nil
	case "t":
		m.overlay = overlayStatsMenu
		return m, nil
	case "p":
		return m.cycleView(), nil
	case "S":
		m.overlay = overlaySettings
		m.settingsField = 0
		return m, nil
	case "a":
		// Route to wizard — handled by cmd package via process exit.
		return m, tea.Quit
	case "e", "d":
		// Edit/remove selected provider — stub for now, menu in overlay.
		if m.focused == panelStatus && m.sel[panelStatus] < len(m.cfg.Providers) {
			m.overlay = overlayRemoveProvider
			m.overlayData = m.cfg.Providers[m.sel[panelStatus]].Name
		}
		return m, nil
	}
	return m, nil
}

func (m model) handleOverlayKey(key string) (tea.Model, tea.Cmd) {
	switch m.overlay {
	case overlayConfirmQuit:
		switch key {
		case "y", "enter":
			return m, tea.Quit
		default:
			m.overlay = overlayNone
			return m, nil
		}
	case overlayConfirmReset:
		switch key {
		case "y", "enter":
			// Zero counters for all providers.
			for _, ps := range m.st.Providers {
				ps.Counters = state.Counters{}
				for _, ms := range ps.Models {
					ms.Counters = state.Counters{}
				}
			}
			_ = m.st.Save()
			m.footer = "counters reset"
			m.overlay = overlayNone
			return m, nil
		default:
			m.overlay = overlayNone
			return m, nil
		}
	case overlayHelp, overlayInspect:
		if key == "esc" || key == "q" || key == "?" || key == "i" {
			m.overlay = overlayNone
		}
		return m, nil
	case overlayStatusMenu, overlayUsageMenu, overlayFavMenu, overlayStatsMenu:
		return m.handleMenuKey(key)
	case overlaySettings:
		if key == "esc" || key == "S" {
			m.overlay = overlayNone
		}
		return m, nil
	case overlayRemoveProvider:
		switch key {
		case "y", "enter":
			name := m.overlayData
			p := m.cfg.Find(name)
			if p != nil {
				_ = secret.Delete(p.KeyRef)
				m.cfg.Remove(name)
				_ = config.Save(m.cfg)
				m.footer = fmt.Sprintf("removed %s", name)
			}
			m.overlay = overlayNone
			return m, nil
		default:
			m.overlay = overlayNone
			return m, nil
		}
	}
	m.overlay = overlayNone
	return m, nil
}

func (m model) handleMenuKey(key string) (tea.Model, tea.Cmd) {
	if key == "esc" || key == "q" {
		m.overlay = overlayNone
		return m, nil
	}
	// Stats menu: r = reset counters.
	if m.overlay == overlayStatsMenu && key == "r" {
		m.overlay = overlayConfirmReset
		return m, nil
	}
	m.overlay = overlayNone
	return m, nil
}

func (m model) handleMouse(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	// Basic click-to-focus; full hit-region tracking is layout-dependent.
	return m, nil
}

// moveSelection moves the selection in the focused pane, clamping.
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
	// Clamp scroll to keep selection visible.
	visRows := m.paneHeight()
	if visRows <= 0 {
		return
	}
	if m.sel[m.focused] < m.scroll[m.focused] {
		m.scroll[m.focused] = m.sel[m.focused]
	}
	if m.sel[m.focused] >= m.scroll[m.focused]+visRows {
		m.scroll[m.focused] = m.sel[m.focused] - visRows + 1
	}
}

func (m model) maxSelection(p panelID) int {
	switch p {
	case panelStatus:
		return len(m.cfg.Providers) - 1
	case panelFavourites:
		n := 0
		for _, pv := range m.cfg.Providers {
			for _, mod := range pv.Models {
				if mod.Favourite {
					n++
				}
			}
		}
		return n - 1
	case panelUsage:
		n := 0
		for _, pv := range m.cfg.Providers {
			n += len(pv.Meters)
		}
		return n - 1
	case panelStats:
		return len(m.st.Providers) - 1
	}
	return 0
}

func (m model) paneHeight() int {
	h := m.height - 4 // header + footer + borders
	if h < 1 {
		return 1
	}
	return h
}

// startCheckAll enqueues probes for all enabled providers + favourites.
func (m model) startCheckAll() tea.Cmd {
	if m.checking {
		return nil
	}
	m.checking = true

	cfg := m.cfg
	timeout := time.Duration(cfg.Settings.ProbeTimeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	type job struct {
		prov    config.Provider
		modelID string
	}
	var jobs []job
	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		jobs = append(jobs, job{p, ""})
		for _, mod := range p.Models {
			if mod.Favourite {
				jobs = append(jobs, job{p, mod.ID})
			}
		}
	}
	if len(jobs) == 0 {
		m.checking = false
		return nil
	}
	m.pendingCount = len(jobs)

	// Build commands: one per job, run concurrently by bubbletea's Batch.
	cmds := make([]tea.Cmd, len(jobs))
	for i, j := range jobs {
		j := j
		cmds[i] = func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			key, _ := secret.Resolve(j.prov.KeyRef)
			adapter := provider.New(j.prov.Kind)
			doer := check.NewDoer(timeout, key)
			var res check.Result
			if j.modelID == "" {
				res = adapter.Probe(ctx, doer, j.prov.BaseURL)
			} else {
				res = adapter.ProbeModel(ctx, doer, j.prov.BaseURL, j.modelID)
			}
			return checkResultMsg{provider: j.prov.Name, modelID: j.modelID, result: res}
		}
	}
	// Cap concurrency at PoolSize by batching in groups.
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
	cmds = append(cmds, m.spinnerCmd())
	return tea.Batch(cmds...)
}

// handleCheckResult records one probe result.
func (m model) handleCheckResult(msg checkResultMsg) (tea.Model, tea.Cmd) {
	scr := state.CheckResult{
		Status:    string(msg.result.Status),
		Reason:    msg.result.Reason,
		HTTPCode:  msg.result.HTTPCode,
		LatencyMs: msg.result.LatencyMs,
		CheckedAt: msg.result.CheckedAt,
	}
	// Detect down transition for alert bell.
	if msg.modelID == "" && m.cfg.Settings.AlertBell {
		prev := "unknown"
		if ps := m.st.Providers[msg.provider]; ps != nil && ps.LastCheck != nil {
			prev = ps.LastCheck.Status
		}
		if prev != "down" && scr.Status == "down" {
			fmt.Fprint(os.Stderr, "\a")
		}
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

// cycleView advances to the next view preset.
func (m model) cycleView() model {
	views := builtinViewNames()
	// Append user views.
	for _, v := range m.cfg.Views {
		views = append(views, v.Name)
	}
	current := m.st.UI.View
	if current == "" {
		current = "full"
	}
	next := "full"
	for i, v := range views {
		if v == current && i+1 < len(views) {
			next = views[i+1]
			break
		} else if v == current && i+1 == len(views) {
			next = views[0]
		}
	}
	m.st.UI.View = next
	_ = m.st.Save()
	return m
}

// builtinViewNames returns the builtin view names in cycle order.
func builtinViewNames() []string {
	return []string{"full", "compact", "status-only", "stats-only"}
}

// activeView resolves the current view definition.
func (m model) activeView() config.View {
	name := m.st.UI.View
	if name == "" {
		name = "full"
	}
	// Check builtins first.
	for _, bv := range builtinViews() {
		if bv.Name == name {
			return bv
		}
	}
	// Then user views.
	for _, v := range m.cfg.Views {
		if v.Name == name {
			return v
		}
	}
	// Unknown → full.
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

// render produces the full dashboard frame.
func (m model) render() string {
	// Stack layout when narrow.
	stack := m.width < 100 || m.height < 24
	view := m.activeView()

	header := m.renderHeader()
	footer := m.renderFooter()

	var body string
	if m.zoomed {
		body = m.renderPane(m.focused, m.width, m.paneHeight())
	} else if stack || view.Arrangement == "stack" {
		body = m.renderStack(view)
	} else {
		body = m.renderGrid(view)
	}

	frame := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)

	if m.overlay != overlayNone {
		frame = m.renderOverlay(frame)
	}
	return frame
}

// renderHeader renders the title bar.
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
	dots := fmt.Sprintf("●%d ◐%d ○%d", ok, account, down)
	viewName := m.activeView().Name
	clock := time.Now().Format("15:04")
	header := fmt.Sprintf(" sslug %s  [p]reset: %-12s %s", dots, viewName, clock)
	// Pad to full width.
	if len(header) < m.width {
		header += strings.Repeat(" ", m.width-len(header))
	}
	return header
}

// renderFooter renders the key hint bar.
func (m model) renderFooter() string {
	if m.footer != "" {
		return " " + m.footer
	}
	hints := " q quit · c check · ⏎ check · s/u/f/t menus · p views · S settings · tab focus · z zoom · ? help"
	return hints
}

// renderGrid renders the 2-column grid layout.
func (m model) renderGrid(view config.View) string {
	split := view.MainSplit
	if split < 0.4 || split > 0.8 {
		split = 0.66
	}
	// Account for header (1 line) + footer (1 line) + pane borders (2 per pane).
	availH := m.height - 2
	halfH := availH / 2
	leftW := int(float64(m.width) * split)
	rightW := m.width - leftW

	left := lipgloss.JoinVertical(lipgloss.Left,
		m.renderPane(panelStatus, leftW, halfH),
		m.renderPane(panelFavourites, leftW, availH-halfH),
	)
	right := lipgloss.JoinVertical(lipgloss.Left,
		m.renderPane(panelUsage, rightW, halfH),
		m.renderPane(panelStats, rightW, availH-halfH),
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// renderStack renders the vertical stack layout.
func (m model) renderStack(view config.View) string {
	var parts []string
	for _, panelName := range view.Panels {
		p := panelByName(panelName)
		if p >= 0 {
			parts = append(parts, m.renderPane(p, m.width, m.paneHeight()/len(view.Panels)))
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// panelByName maps a panel name to its ID, or -1.
func panelByName(name string) panelID {
	for i, n := range panelNames {
		if n == name {
			return panelID(i)
		}
	}
	return -1
}

// renderPane renders one pane box with title and content.
func (m model) renderPane(p panelID, w, h int) string {
	title := panelNames[p]
	focused := m.focused == p
	if focused {
		title = "[*] " + title
	} else {
		title = "[ ] " + title
	}

	// Clamp inner dimensions.
	innerW := w - 2
	innerH := h - 2
	if innerW < 4 {
		innerW = 4
	}
	if innerH < 1 {
		innerH = 1
	}

	content := m.paneContent(p, innerW, innerH)

	// Truncate content lines to inner width.
	contentLines := strings.Split(content, "\n")
	for i, line := range contentLines {
		if len(line) > innerW {
			contentLines[i] = line[:innerW]
		}
	}
	// Pad or trim to exact height.
	for len(contentLines) < innerH {
		contentLines = append(contentLines, "")
	}
	if len(contentLines) > innerH {
		contentLines = contentLines[:innerH]
	}
	content = strings.Join(contentLines, "\n")

	borderColor := m.palette[theme.BoxBorder]
	if focused {
		borderColor = m.palette[theme.BoxBorderFocus]
	}
	borderStyle := lipgloss.RoundedBorder()
	if m.cfg.Settings.BorderStyle == "square" {
		borderStyle = lipgloss.NormalBorder()
	} else if m.cfg.Settings.BorderStyle == "thick" {
		borderStyle = lipgloss.ThickBorder()
	}

	style := lipgloss.NewStyle().
		Border(borderStyle).
		BorderForeground(lipgloss.Color(borderColor)).
		Width(innerW).
		Height(innerH)

	box := style.Render(content)

	// Replace the top border line to embed the title.
	lines := strings.Split(box, "\n")
	if len(lines) > 0 && len(title)+4 < w {
		top := lines[0]
		// Find where the corner ends and insert the title.
		if len(top) >= 4 {
			// Keep corner char + one border char, then title, then fill rest.
			titleStr := "─ " + title + " "
			remaining := w - 2 - len(titleStr) // -2 for corners
			if remaining < 0 {
				remaining = 0
			}
			fill := strings.Repeat("─", remaining)
			lines[0] = string([]rune(top)[0]) + titleStr + fill + string([]rune(top)[len([]rune(top))-1])
		}
	}
	return strings.Join(lines, "\n")
}

// paneContent renders the inner content of a pane.
func (m model) paneContent(p panelID, w, h int) string {
	if w < 4 {
		w = 4
	}
	if h < 1 {
		h = 1
	}
	switch p {
	case panelStatus:
		return m.renderStatusPane(w, h)
	case panelUsage:
		return m.renderUsagePane(w, h)
	case panelFavourites:
		return m.renderFavouritesPane(w, h)
	case panelStats:
		return m.renderStatsPane(w, h)
	}
	return ""
}

// renderStatusPane renders the provider health rows.
func (m model) renderStatusPane(w, h int) string {
	if len(m.cfg.Providers) == 0 {
		return "no providers — press a to add"
	}
	var lines []string
	for i, p := range m.cfg.Providers {
		dot := "◌"
		status := "unknown"
		latency := ""
		age := ""
		reason := ""
		if ps := m.st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
			lc := ps.LastCheck
			switch lc.Status {
			case "ok":
				dot = "●"
			case "account":
				dot = "◐"
			case "down":
				dot = "○"
			}
			status = lc.Status
			reason = lc.Reason
			if lc.LatencyMs > 0 {
				latency = fmt.Sprintf("%.0fms", lc.LatencyMs)
			}
			if !lc.CheckedAt.IsZero() {
				age = state.RelAge(time.Since(lc.CheckedAt))
			}
		}
		if m.checking {
			spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			dot = spinner[m.spinnerIdx%len(spinner)]
		}

		line := fmt.Sprintf("%s %-20s %-8s %6s  %s", dot, p.Name, status, latency, age)
		if reason != "" && status != "ok" {
			line += "  " + reason
		}
		// Highlight selected.
		if i == m.sel[panelStatus] && m.focused == panelStatus {
			line = "> " + line
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	return renderScrollable(lines, m.scroll[panelStatus], h)
}

// renderUsagePane renders meter/usage blocks.
func (m model) renderUsagePane(w, h int) string {
	var lines []string
	for _, p := range m.cfg.Providers {
		// No Enabled filter — disabled providers still show their meters.
		dot := "◌"
		if ps := m.st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
			switch ps.LastCheck.Status {
			case "ok":
				dot = "●"
			case "account":
				dot = "◐"
			case "down":
				dot = "○"
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s", dot, p.Name))
		if p.Note != "" {
			lines = append(lines, "  "+p.Note)
		}
		for _, meter := range p.Meters {
			mv := m.st.GetMeter(p.Name, meter.Name)
			val := meter.Used
			var setAt time.Time
			if mv != nil {
				val = mv.Value
				setAt = mv.SetAt
			}
			line := fmt.Sprintf("  %s %.4g", meter.Name, val)
			if meter.Cap > 0 {
				line += fmt.Sprintf("/%.4g", meter.Cap)
			}
			line += " " + meter.Unit
			if !setAt.IsZero() {
				age := state.RelAge(time.Since(setAt))
				if meter.Kind == "manual" {
					line += "  · set " + age
				} else {
					line += "  · " + age
				}
			}
			lines = append(lines, line)

			// Cap bar.
			if meter.Cap > 0 && w > 10 {
				pct := val / meter.Cap
				if pct > 1 {
					pct = 1
				}
				barW := w - 6
				filled := int(pct * float64(barW))
				bar := strings.Repeat("▓", filled) + strings.Repeat("░", barW-filled)
				lines = append(lines, "  "+bar)
			}

			// Reset line.
			if meter.Reset != "" && meter.Reset != "never" {
				resetLine := resetDescription(meter.Reset, time.Now())
				if resetLine != "" {
					lines = append(lines, "  "+resetLine)
				}
			}
		}
		// Probe counters.
		if ps := m.st.Providers[p.Name]; ps != nil {
			c := ps.Counters
			if c.Checks > 0 {
				lines = append(lines, fmt.Sprintf("  probes: %d ok / %d account / %d down", c.OK, c.Account, c.Down))
			}
		}
		lines = append(lines, "")
	}
	return renderScrollable(lines, m.scroll[panelUsage], h)
}

// renderFavouritesPane renders the latency cockpit.
func (m model) renderFavouritesPane(w, h int) string {
	type fav struct {
		provider string
		model    config.Model
	}
	var favs []fav
	for _, p := range m.cfg.Providers {
		for _, mod := range p.Models {
			if mod.Favourite {
				favs = append(favs, fav{p.Name, mod})
			}
		}
	}
	if len(favs) == 0 {
		return "no favourites — press f to add"
	}
	sparkW := w / 3
	if sparkW > 20 {
		sparkW = 20
	}
	if sparkW < 4 {
		sparkW = 4
	}
	var lines []string
	for i, f := range favs {
		name := f.model.ID
		if f.model.Alias != "" {
			name = f.model.Alias
		}
		if len(name) > 18 {
			name = name[:18]
		}
		dot := "◌"
		latency := ""
		age := ""
		var ring []float64
		if ps := m.st.Providers[f.provider]; ps != nil {
			if ms := ps.Models[f.model.ID]; ms != nil {
				ring = ms.Ring
				if ms.LastCheck != nil {
					switch ms.LastCheck.Status {
					case "ok":
						dot = "●"
					case "account":
						dot = "◐"
					case "down":
						dot = "○"
					}
					if ms.LastCheck.LatencyMs > 0 {
						latency = fmt.Sprintf("%.0fms", ms.LastCheck.LatencyMs)
					}
					age = state.RelAge(time.Since(ms.LastCheck.CheckedAt))
				}
			}
		}
		spark := Spark(ring, sparkW)
		line := fmt.Sprintf("★ %-18s %s %6s %s %s", name, dot, latency, spark, age)
		if i == m.sel[panelFavourites] && m.focused == panelFavourites {
			line = "> " + line
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	return renderScrollable(lines, m.scroll[panelFavourites], h)
}

// renderStatsPane renders the per-provider stats table.
func (m model) renderStatsPane(w, h int) string {
	if len(m.st.Providers) == 0 {
		return "no data yet"
	}
	header := fmt.Sprintf("%-20s %6s %5s %6s %6s %4s %s", "name", "checks", "ok%", "p50", "p95", "↓", "ago")
	lines := []string{header}
	for _, pv := range m.cfg.Providers {
		ps := m.st.Providers[pv.Name]
		if ps == nil {
			continue
		}
		c := ps.Counters
		if c.Checks == 0 {
			continue
		}
		okPct := 0
		if c.Checks > 0 {
			okPct = c.OK * 100 / c.Checks
		}
		p50, p95 := percentile(ps.Ring, 50), percentile(ps.Ring, 95)
		age := ""
		if ps.LastCheck != nil {
			age = state.RelAge(time.Since(ps.LastCheck.CheckedAt))
		}
		row := fmt.Sprintf("%-20s %6d %4d%% %6.0f %6.0f %4d %s",
			pv.Name, c.Checks, okPct, p50, p95, c.Down, age)
		lines = append(lines, row)
		// Favourite model rows.
		for _, mod := range pv.Models {
			if !mod.Favourite {
				continue
			}
			if ms := ps.Models[mod.ID]; ms != nil && ms.Counters.Checks > 0 {
				mc := ms.Counters
				mOkPct := mc.OK * 100 / mc.Checks
				mp50, mp95 := percentile(ms.Ring, 50), percentile(ms.Ring, 95)
				mAge := ""
				if ms.LastCheck != nil {
					mAge = state.RelAge(time.Since(ms.LastCheck.CheckedAt))
				}
				sub := fmt.Sprintf("  ☆ %-16s %6d %4d%% %6.0f %6.0f %4d %s",
					mod.ID, mc.Checks, mOkPct, mp50, mp95, mc.Down, mAge)
				lines = append(lines, sub)
			}
		}
	}
	if len(lines) == 1 {
		return "no data yet"
	}
	return renderScrollable(lines, m.scroll[panelStats], h)
}

// percentile returns the p-th percentile of ring (ms).
func percentile(ring []float64, p int) float64 {
	if len(ring) == 0 {
		return 0
	}
	sorted := make([]float64, len(ring))
	copy(sorted, ring)
	// Insertion sort (rings are small).
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	idx := int(float64(len(sorted)) * float64(p) / 100.0)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// renderScrollable renders lines with scroll offset, clamped to height.
func renderScrollable(lines []string, offset, h int) string {
	if offset >= len(lines) {
		offset = len(lines) - 1
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + h
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[offset:end]
	// Pad to height.
	for len(visible) < h {
		visible = append(visible, "")
	}
	return strings.Join(visible, "\n")
}

// renderOverlay composites an overlay on top of the frame.
func (m model) renderOverlay(base string) string {
	var content string
	switch m.overlay {
	case overlayHelp:
		content = boxStyle(m).Render("Help\n\n" + m.overlayData + "\n\nesc to close")
	case overlayInspect:
		content = boxStyle(m).Render("Inspect\n\n" + m.overlayData + "\n\nesc to close")
	case overlayConfirmQuit:
		content = boxStyle(m).Render("Quit sslug?\n\ny / n")
	case overlayConfirmReset:
		content = boxStyle(m).Render("Reset all counters?\n\ny / n")
	case overlayStatusMenu:
		content = boxStyle(m).Render("Status menu\n\nr = refresh\nesc = close")
	case overlayUsageMenu:
		content = boxStyle(m).Render("Usage menu\n\nesc = close")
	case overlayFavMenu:
		content = boxStyle(m).Render("Favourites menu\n\nesc = close")
	case overlayStatsMenu:
		content = boxStyle(m).Render("Stats menu\n\nr = reset counters\nesc = close")
	case overlaySettings:
		content = boxStyle(m).Render(m.settingsText())
	case overlayRemoveProvider:
		content = boxStyle(m).Render(fmt.Sprintf("Remove provider %q?\n\ny / n", m.overlayData))
	}
	// Center overlay on the base frame.
	lines := strings.Split(base, "\n")
	overlayLines := strings.Split(content, "\n")
	mid := len(lines) / 2
	start := mid - len(overlayLines)/2
	if start < 0 {
		start = 0
	}
	for i, ol := range overlayLines {
		idx := start + i
		if idx < len(lines) {
			lines[idx] = ol
		}
	}
	return strings.Join(lines, "\n")
}

func boxStyle(m model) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.palette[theme.BoxBorderFocus])).
		Padding(0, 1)
}

// settingsText renders the settings overlay content.
func (m model) settingsText() string {
	s := m.cfg.Settings
	return fmt.Sprintf(`Settings (edit config.toml, then restart)

  theme              %s
  probe_timeout      %ds
  auto_refresh       %ds
  probe_mode         %s
  history_length     %d
  keys_source        %s
  nerd_font          %v
  confirm_quit       %v
  check_on_launch    %v
  alert_bell         %v
  border_style       %s
  graph_glyphs       %s

esc to close`,
		s.Theme, s.ProbeTimeout, s.AutoRefresh, s.ProbeMode,
		s.HistoryLength, s.KeysSource, s.NerdFont, s.ConfirmQuit,
		s.CheckOnLaunch, s.AlertBell, s.BorderStyle, s.GraphGlyphs)
}

// inspectText renders the inspect overlay for the selected row.
func (m model) inspectText() string {
	if m.focused == panelStatus && m.sel[panelStatus] < len(m.cfg.Providers) {
		p := m.cfg.Providers[m.sel[panelStatus]]
		ps := m.st.Providers[p.Name]
		var sb strings.Builder
		fmt.Fprintf(&sb, "Provider: %s\n", p.Name)
		fmt.Fprintf(&sb, "Kind:     %s\n", p.Kind)
		fmt.Fprintf(&sb, "Base URL: %s\n", p.BaseURL)
		fmt.Fprintf(&sb, "Key ref:  %s\n", secret.Redact(p.KeyRef))
		fmt.Fprintf(&sb, "Enabled:  %v\n", p.Enabled)
		if ps != nil && ps.LastCheck != nil {
			lc := ps.LastCheck
			fmt.Fprintf(&sb, "\nLast check:\n")
			fmt.Fprintf(&sb, "  Status:   %s\n", lc.Status)
			fmt.Fprintf(&sb, "  Reason:   %s\n", lc.Reason)
			fmt.Fprintf(&sb, "  HTTP:     %d\n", lc.HTTPCode)
			fmt.Fprintf(&sb, "  Latency:  %.0fms\n", lc.LatencyMs)
			fmt.Fprintf(&sb, "  Checked:  %s\n", lc.CheckedAt.Format("2006-01-02 15:04:05 UTC"))
		}
		if ps != nil && len(ps.Ring) > 0 {
			p50, p95 := percentile(ps.Ring, 50), percentile(ps.Ring, 95)
			fmt.Fprintf(&sb, "\nLatency p50=%.0fms  p95=%.0fms  (n=%d)\n", p50, p95, len(ps.Ring))
		}
		return sb.String()
	}
	return "select a provider row to inspect"
}

// resetDescription renders a human reset description.
func resetDescription(spec string, now time.Time) string {
	if spec == "" || spec == "never" {
		return ""
	}
	next := provider.NextResetForTest(spec, now)
	d := time.Until(next)
	switch {
	case d < 0:
		return "overdue since " + next.Format("Jan 2")
	case d < 24*time.Hour:
		return fmt.Sprintf("resets in %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("resets in %dd", int(d.Hours()/24))
	}
}

const helpText = `sslug keymap

  tab / shift-tab   cycle pane focus
  j / k             scroll down / up
  PgUp / PgDn       scroll faster
  c or enter        check all providers
  i                 inspect selected row
  s / u / f / t     panel menus
  p                 cycle view presets
  S                 settings
  a                 add provider (wizard)
  e / d             edit / remove selected
  z                 zoom focused pane
  ?                 this help
  q                 quit

View config file: sslug config path`
