package dashboard

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
)

// glyphSet holds the status/favourite glyphs for the current settings.
type glyphSet struct {
	ok, account, down, unknown string
}

// unicodeGlyphs is the default, terminal-safe set.
var unicodeGlyphs = glyphSet{ok: "●", account: "◐", down: "○", unknown: "◌"}

// nerdGlyphs is the nerd_font=true set (Nerd Font codepoints).
var nerdGlyphs = glyphSet{ok: "\uf00c", account: "\uf06a", down: "\uf00d", unknown: "\uf128"}

// glyphs returns the active glyph set per settings.nerd_font.
func (m model) glyphs() glyphSet {
	if m.cfg.Settings.NerdFont {
		return nerdGlyphs
	}
	return unicodeGlyphs
}

// latencyStyle colors a latency reading by threshold: fast green, mid muted,
// slow amber — btop's data-by-value discipline.
func (m model) latencyStyle(ms float64) lipgloss.Style {
	c := m.palette[theme.Muted]
	if ms > 0 && ms < 300 {
		c = m.palette[theme.OK]
	} else if ms >= 1000 {
		c = m.palette[theme.Warn]
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
}

func displayAge(age string) string {
	return strings.TrimSuffix(age, " ago")
}

func (m model) reliabilityStyle(percent int) lipgloss.Style {
	role := theme.Err
	if percent >= 85 {
		role = theme.OK
	} else if percent >= 60 {
		role = theme.Warn
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[role])).Bold(true)
}

// glyphFor maps a status string to the pane glyph.
func (m model) glyphFor(status string) string {
	g := m.glyphs()
	switch status {
	case "ok":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.OK])).Render(g.ok)
	case "account":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Warn])).Render(g.account)
	case "down":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Err])).Render(g.down)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Unknown])).Render(g.unknown)
}

// probeGlyph returns the spinner frame while checking, else the status glyph.
func (m model) probeGlyph(status string) string {
	if m.checking {
		return m.spin.View()
	}
	return m.glyphFor(status)
}

// --- status pane ---

func (m model) renderStatusPane(w, h int, _ bool) string {
	providers := m.sortedProviders()
	if m.prefs.statusGroup {
		providers = groupedProviders(providers)
	}
	if len(providers) == 0 {
		return m.renderEmptyStatus(w, h)
	}
	contentWidth := max(1, w-1)
	if contentWidth < 70 {
		return m.renderNarrowStatus(providers, contentWidth, h)
	}
	rightWidth := 42
	if contentWidth >= 120 {
		rightWidth = 58
	}
	graphWidth := max(24, contentWidth-rightWidth-2)
	rightWidth = contentWidth - graphWidth - 2
	selectedIndex := min(max(0, m.sel[panelStatus]), len(providers)-1)
	selectedProvider := providers[selectedIndex]
	var history []float64
	if providerState := m.st.Providers[selectedProvider.Name]; providerState != nil {
		history = providerState.Ring
	}
	graph := m.statusGraph(history, selectedProvider, graphWidth, h)
	if graphWidth >= 60 {
		detailWidth := min(34, graphWidth/2)
		for index, detail := range m.statusDetailLines(selectedProvider) {
			row := index + 1
			if row >= len(graph)-1 {
				break
			}
			graph[row] = fitCells(detail, detailWidth) + ansi.Cut(graph[row], detailWidth, graphWidth)
		}
	}
	right := make([]string, h)
	right[0] = m.statusHeaderLine(rightWidth)
	statusRows := m.statusViewportLines(providers, rightWidth)
	visibleRows := max(0, h-1)
	offset := min(m.scroll[panelStatus], max(0, len(statusRows)-visibleRows))
	for rowIndex, line := range statusRows[offset:min(len(statusRows), offset+visibleRows)] {
		right[rowIndex+1] = line
	}
	lines := make([]string, h)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	for row := range h {
		lines[row] = fitCells(graph[row], graphWidth) + muted.Render("  ") + fitCells(right[row], rightWidth)
		lines[row] = fitCells(lines[row], contentWidth) +
			viewportScrollCell(len(statusRows), offset, visibleRows, row, h, m.palette, theme.PaneStatus)
	}
	return strings.Join(lines, "\n")
}

func (m model) statusDetailLines(providerConfig *config.Provider) []string {
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	lines := []string{title.Render(providerConfig.Name)}
	providerState := m.st.Providers[providerConfig.Name]
	if providerState == nil {
		return append(lines, muted.Render("not measured"))
	}
	if providerState.LastCheck != nil {
		lines = append(lines, muted.Render(providerState.LastCheck.Status+" · "+providerState.LastCheck.Reason))
	}
	counter := providerState.Counters
	lines = append(lines,
		muted.Render(fmt.Sprintf("checks %d · ok %d · account %d · down %d", counter.Checks, counter.OK, counter.Account, counter.Down)),
		muted.Render(fmt.Sprintf("p50 %.0fms · p95 %.0fms · samples %d", percentile(providerState.Ring, 50), percentile(providerState.Ring, 95), len(providerState.Ring))),
		muted.Render(fmt.Sprintf("models %d · errors %d", len(providerState.Models), len(providerState.RecentErrors))),
	)
	return lines
}

func (m model) renderEmptyStatus(width, height int) string {
	contentWidth := max(1, width-1)
	lines := []string{strings.Repeat(" ", contentWidth)}
	for _, line := range m.moshiDashboardLines(contentWidth) {
		if len(lines) >= height {
			break
		}
		lines = append(lines, line)
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", contentWidth))
	}
	for row := range height {
		lines[row] = fitCells(lines[row], contentWidth) + " "
	}
	return strings.Join(lines, "\n")
}

func (m model) renderNarrowStatus(providers []*config.Provider, w, h int) string {
	statusRows := m.statusViewportLines(providers, w)
	visibleRows := max(0, h-1)
	offset := min(m.scroll[panelStatus], max(0, len(statusRows)-visibleRows))
	lines := []string{m.statusHeaderLine(w)}
	lines = append(lines, statusRows[offset:min(len(statusRows), offset+visibleRows)]...)
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	for row := range h {
		lines[row] = fitCells(lines[row], w) +
			viewportScrollCell(len(statusRows), offset, visibleRows, row, h, m.palette, theme.PaneStatus)
	}
	return strings.Join(lines, "\n")
}

func (m model) statusViewportLines(providers []*config.Provider, width int) []string {
	lines := append([]string(nil), m.moshiDashboardLines(width)...)
	for index, provider := range providers {
		lines = append(lines, m.statusGaugeLine(provider, width, index == m.sel[panelStatus]))
	}
	return lines
}

func groupedProviders(providers []*config.Provider) []*config.Provider {
	out := append([]*config.Provider(nil), providers...)
	key := func(provider *config.Provider) string {
		label := provider.Label
		if label == "" {
			label = provider.Kind
		}
		return label
	}
	for index := 1; index < len(out); index++ {
		for cursor := index; cursor > 0 && key(out[cursor]) < key(out[cursor-1]); cursor-- {
			out[cursor], out[cursor-1] = out[cursor-1], out[cursor]
		}
	}
	return out
}

func (m model) statusHeaderLine(width int) string {
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	nameWidth, ageWidth := 18, 0
	if width >= 46 {
		ageWidth = 9
	}
	line := "  " + title.Render(statsCell("provider/model", nameWidth)) + title.Render(statsCell("p95", 6))
	if ageWidth > 0 {
		line += title.Render(statsCell("age", ageWidth))
	}
	return fitCells(line+title.Render("gauge"), width)
}

func (m model) statusGaugeLine(provider *config.Provider, width int, selected bool) string {
	status := "unknown"
	latency, p95 := 0.0, 0.0
	age := ""
	if providerState := m.st.Providers[provider.Name]; providerState != nil {
		p95 = percentile(providerState.Ring, 95)
		if providerState.LastCheck != nil {
			latency = providerState.LastCheck.LatencyMs
			if !providerState.LastCheck.CheckedAt.IsZero() {
				age = state.RelAge(time.Since(providerState.LastCheck.CheckedAt))
			}
		}
	}
	if !provider.Enabled {
		status = "unknown"
	}
	mark := m.probeGlyph(status)
	nameWidth := 18
	showAge := width >= 46
	ageWidth := 0
	if showAge {
		ageWidth = 9
	}
	p95Text := "—"
	if p95 > 0 {
		p95Text = fmt.Sprintf("%.0f", p95)
	}
	prefixWidth := 2 + nameWidth
	gaugeWidth := max(5, width-prefixWidth-6-ageWidth-2)
	nameColor := statusColor(m.palette, status)
	if status == "unknown" {
		nameColor = m.palette[theme.Title]
	}
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(nameColor)).Bold(true)
	line := mark + " " + nameStyle.Render(statsCell(provider.Name, nameWidth)) +
		lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true).Render(statsCell(p95Text, 6))
	if showAge {
		line += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted])).Render(statsCell(displayAge(age), ageWidth))
	}
	line += m.latencyGauge(max(latency, p95), gaugeWidth)
	line = fitCells(line, width)
	if selected && m.focused == panelStatus {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
			Background(lipgloss.Color(m.palette[theme.SelectedBg])).
			Bold(true).
			Render(fitCells(ansi.Strip(line), width))
	}
	return line
}

func (m model) latencyGauge(latency float64, width int) string {
	ratio := clampUnit(latency / 600)
	filled := int(ratio*float64(width) + 0.5)
	fill := m.latencyStyle(latency).Render(strings.Repeat("▰", filled))
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.BarEmpty])).Render(strings.Repeat("▱", width-filled))
	return fill + empty
}

func clampUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (m model) statusGraph(history []float64, provider *config.Provider, width, height int) []string {
	lines := make([]string, height)
	if height == 0 {
		return lines
	}
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Err])).Bold(true)
	errorSeries := make([]float64, max(1, width))
	errorCount := 0
	window := 30 * time.Minute
	now := time.Now()
	if providerState := m.st.Providers[provider.Name]; providerState != nil {
		for _, recent := range providerState.RecentErrors {
			age := now.Sub(recent.CheckedAt)
			if age < 0 || age > window {
				continue
			}
			fraction := float64(window-age) / float64(window)
			index := int(fraction * float64(len(errorSeries)-1))
			errorSeries[index]++
			errorCount++
		}
	}
	leftHead := title.Render("p95 "+truncate(provider.Name, 12)) + muted.Render(" 600ms")
	rightHead := errStyle.Render("errors") + muted.Render(fmt.Sprintf(" %d", errorCount))
	lines[0] = leftHead + strings.Repeat(" ", max(1, width-ansi.StringWidth(leftHead)-ansi.StringWidth(rightHead))) + rightHead
	if height == 1 {
		return lines
	}
	graphRows := max(1, height-2)
	upperRows := max(1, graphRows*2/3)
	lowerRows := graphRows - upperRows
	upper := PairGraph(history, width, upperRows, 600, m.cfg.Settings.GraphStyle, false)
	lower := PairGraph(errorSeries, width, lowerRows, 5, m.cfg.Settings.GraphStyle, true)
	for row, graphLine := range upper {
		ratio := float64(upperRows-row) / float64(max(1, upperRows))
		role := theme.OK
		if ratio >= 0.80 {
			role = theme.Err
		} else if ratio >= 0.55 {
			role = theme.Warn
		}
		lines[row+1] = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[role])).Render(graphLine)
	}
	for row, graphLine := range lower {
		lines[1+upperRows+row] = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Err])).Render(graphLine)
	}
	lines[height-1] = muted.Render("30m") + strings.Repeat(" ", max(0, width-6)) + muted.Render("now")
	return lines
}

func statusColor(pal theme.Palette, status string) string {
	switch status {
	case "ok":
		return pal[theme.OK]
	case "account":
		return pal[theme.Warn]
	case "down":
		return pal[theme.Err]
	}
	return pal[theme.Unknown]
}

func statusReasonSummary(status, reason, kind, baseURL string) string {
	switch status {
	case "ok":
		if kind != "" {
			return kind + " · live endpoint"
		}
	case "disabled":
		return reason
	case "account":
		if strings.Contains(reason, "401") || strings.Contains(reason, "403") {
			return "account/auth problem — key or entitlement needs attention"
		}
		if strings.Contains(reason, "402") || strings.Contains(strings.ToLower(reason), "quota") {
			return "account/quota problem — billing or quota limit"
		}
		return "account issue — " + reason
	case "down":
		low := strings.ToLower(reason)
		switch {
		case strings.Contains(low, "timeout"):
			return "network timeout — provider did not answer in time"
		case strings.Contains(low, "dns"):
			return "network/DNS failure"
		case strings.Contains(low, "5xx") || strings.Contains(low, "http 5") ||
			strings.Contains(low, "status 5") || strings.Contains(low, "server error"):
			return "provider/server failure"
		case reason != "":
			return "probe failed — " + reason
		}
	case "unknown":
		if baseURL != "" {
			return "not checked yet · " + baseURL
		}
	}
	return ""
}

// --- usage pane ---

// usageEntry is one provider's meter block or header line in the usage pane.
type usageEntry struct {
	provider string
	meter    *config.Meter // nil = provider header
}

func (m model) usageEntries() []usageEntry {
	var out []usageEntry
	provs := m.sortedProviders()
	if m.prefs.usageSortAlpha {
		// already name-sorted by default
	}
	for _, p := range provs {
		out = append(out, usageEntry{provider: p.Name})
		for i := range p.Meters {
			out = append(out, usageEntry{provider: p.Name, meter: &p.Meters[i]})
		}
	}
	return out
}

func (m model) renderUsagePane(w, h int, _ bool) string {
	if len(m.cfg.Providers) == 0 {
		return ""
	}
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	contentWidth := max(1, w-1)
	var lines []string
	entryIndex := 0
	for _, providerConfig := range m.sortedProviders() {
		status := "unknown"
		providerState := m.st.Providers[providerConfig.Name]
		if providerState != nil && providerState.LastCheck != nil {
			status = providerState.LastCheck.Status
		}
		header := m.glyphFor(status) + " " + title.Render(providerConfig.Name)
		header = fitCells(header, contentWidth)
		if entryIndex == m.sel[panelUsage] && m.focused == panelUsage {
			header = lipgloss.NewStyle().
				Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
				Background(lipgloss.Color(m.palette[theme.SelectedBg])).
				Bold(true).
				Render(header)
		}
		lines = append(lines, header)
		entryIndex++
		if providerConfig.Note != "" &&
			!strings.HasPrefix(providerConfig.Note, "configured from OMP") &&
			!strings.HasPrefix(providerConfig.Note, "OMP-managed") {
			lines = append(lines, muted.Render(fitCells(providerConfig.Note, contentWidth)))
		}
		if len(providerConfig.Meters) == 0 {
			if providerState != nil && providerState.Counters.Checks > 0 {
				counter := providerState.Counters
				success := float64(counter.OK) / float64(counter.Checks)
				value := title.Render(fmt.Sprintf("probe success  %3.0f%%", success*100))
				lines = append(lines,
					fitCells(value, contentWidth),
					m.meterCells(success, max(6, contentWidth-5))+" "+title.Render(fmt.Sprintf("%3.0f%%", success*100)),
					muted.Render(fitCells(fmt.Sprintf("checks %d · account %d · down %d", counter.Checks, counter.Account, counter.Down), contentWidth)),
				)
			} else {
				lines = append(lines, muted.Render(fitCells("not measured", contentWidth)))
			}
		} else {
			for _, meter := range providerConfig.Meters {
				lines = append(lines, m.meterLines(providerConfig.Name, meter, contentWidth, entryIndex == m.sel[panelUsage])...)
				entryIndex++
			}
			if providerState != nil && providerState.Counters.Checks > 0 {
				counter := providerState.Counters
				lines = append(lines, muted.Render(fitCells(
					fmt.Sprintf("checks %d · ok %d · account %d · down %d", counter.Checks, counter.OK, counter.Account, counter.Down),
					contentWidth)))
			}
		}
	}
	return renderViewport(lines, m.scroll[panelUsage], h, w, m.palette, theme.PaneUsage)
}

// meterLines renders one real meter as value, square-cell gauge, and source.
func (m model) meterLines(providerName string, meter config.Meter, width int, selected bool) []string {
	meterValue := m.st.GetMeter(providerName, meter.Name)
	value := meter.Used
	var setAt time.Time
	if meterValue != nil {
		value = meterValue.Value
		setAt = meterValue.SetAt
	}
	percent := 0.0
	if meter.Cap > 0 {
		percent = clampUnit(value / meter.Cap)
	}
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	label := providerName + " · " + meter.Name
	if meterValue == nil && meter.Auto != "" {
		first := fitCells(title.Render(label), width)
		if selected && m.focused == panelUsage {
			first = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
				Background(lipgloss.Color(m.palette[theme.SelectedBg])).Bold(true).Render(first)
		}
		return []string{first, muted.Render(fitCells("not measured · refresh to measure", width))}
	}
	valueText := fmt.Sprintf("%.4g", value)
	if meter.Cap > 0 {
		valueText += fmt.Sprintf(" / %.4g", meter.Cap)
	} else {
		valueText += " / uncapped"
	}
	if meter.Unit != "" {
		valueText += " " + meter.Unit
	}
	left := title.Render(label)
	valueStyled := title.Render(valueText)
	first := left + strings.Repeat(" ", max(1, width-ansi.StringWidth(left)-ansi.StringWidth(valueStyled))) + valueStyled
	first = fitCells(first, width)
	if selected && m.focused == panelUsage {
		first = lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
			Background(lipgloss.Color(m.palette[theme.SelectedBg])).
			Bold(true).
			Render(first)
	}
	lines := []string{first}
	if meter.Cap > 0 {
		gaugeWidth := max(6, width-5)
		lines = append(lines, m.meterCells(percent, gaugeWidth)+" "+title.Render(fmt.Sprintf("%3.0f%%", percent*100)))
	}
	source := meter.Kind
	if source == "" {
		source = "manual"
	}
	var detail []string
	detail = append(detail, source)
	if meter.Reset != "" && meter.Reset != "never" {
		if reset, _ := resetDescription(meter.Reset, time.Now()); reset != "" {
			detail = append(detail, reset)
		}
	}
	if !setAt.IsZero() {
		detail = append(detail, "set "+state.RelAge(time.Since(setAt)))
	}
	lines = append(lines, muted.Render(fitCells(strings.Join(detail, " · "), width)))
	return lines
}

func (m model) meterCells(percent float64, width int) string {
	percent = clampUnit(percent)
	filled := int(percent*float64(width) + 0.5)
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.BarEmpty]))
	var out strings.Builder
	for index := range width {
		if index >= filled {
			out.WriteString(empty.Render("■"))
			continue
		}
		threshold := float64(index+1) / float64(width)
		role := theme.OK
		if threshold >= 0.85 {
			role = theme.Err
		} else if threshold >= 0.60 {
			role = theme.Warn
		}
		out.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[role])).Render("■"))
	}
	return out.String()
}

func (m model) meterBar(percent float64, width int) string {
	return m.meterCells(percent, width)
}

// resetDescription renders a human reset line and its urgency:
// -1 overdue, 1 imminent (<2 days), 0 otherwise.
func resetDescription(spec string, now time.Time) (string, int) {
	next := provider.NextResetForTest(spec, now)
	d := next.Sub(now)
	switch {
	case spec == "" || spec == "never":
		return "", 0
	case d < 0:
		return "overdue since " + next.Format("Jan 2"), -1
	case d < 24*time.Hour:
		return fmt.Sprintf("cycle resets in %dh", int(d.Hours())), 1
	case d < 48*time.Hour:
		return "cycle resets tomorrow", 1
	default:
		return fmt.Sprintf("cycle resets in %dd", int(d.Hours()/24)), 0
	}
}

// --- favourites pane ---

func (m model) renderFavouritesPane(w, h int, _ bool) string {
	favourites := m.favouriteList()
	if len(favourites) == 0 {
		return ""
	}
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	contentWidth := max(1, w-1)
	compact := contentWidth < 60
	modelWidth, providerWidth, nowWidth, p95Width, ageWidth, graphWidth := 18, 13, 8, 6, 9, 7
	if compact {
		providerWidth = 0
		modelWidth = max(8, contentWidth-nowWidth-p95Width-ageWidth-graphWidth)
	}
	header := title.Render(statsCell("model", modelWidth))
	if !compact {
		header += title.Render(statsCell("provider", providerWidth))
	}
	header += title.Render(statsCell("now", nowWidth)) + title.Render(statsCell("p95", p95Width)) +
		title.Render(statsCell("age", ageWidth)) + title.Render("graph")
	lines := []string{fitCells(header, contentWidth)}
	visibleRows := max(0, h-1)
	offset := min(m.scroll[panelFavourites], max(0, len(favourites)-visibleRows))
	for rowIndex, favourite := range favourites[offset:min(len(favourites), offset+visibleRows)] {
		name := favourite.model.ID
		if favourite.model.Alias != "" {
			name = favourite.model.Alias
		}
		status, latency, p95, age := "unknown", 0.0, 0.0, ""
		var ring []float64
		if providerState := m.st.Providers[favourite.provider.Name]; providerState != nil {
			if modelState := providerState.Models[favourite.model.ID]; modelState != nil {
				ring = modelState.Ring
				p95 = percentile(ring, 95)
				if modelState.LastCheck != nil {
					status = modelState.LastCheck.Status
					latency = modelState.LastCheck.LatencyMs
					if !modelState.LastCheck.CheckedAt.IsZero() {
						age = state.RelAge(time.Since(modelState.LastCheck.CheckedAt))
					}
				}
			}
		}
		graph := PairGraph(ring, graphWidth, 1, 600, m.cfg.Settings.GraphStyle, false)
		graphLine := strings.Repeat(" ", graphWidth)
		if len(graph) > 0 {
			graphLine = graph[0]
		}
		nowText, p95Text, ageText := "—", "—", "—"
		if latency > 0 {
			nowText = fmt.Sprintf("%.0fms", latency)
		}
		if p95 > 0 {
			p95Text = fmt.Sprintf("%.0f", p95)
		}
		if age != "" {
			ageText = age
		}
		rowColor := statusColor(m.palette, status)
		if status == "unknown" {
			rowColor = m.palette[theme.Title]
		}
		rowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(rowColor)).Bold(true)
		line := rowStyle.Render(statsCell(name, modelWidth))
		if !compact {
			line += title.Render(statsCell(favourite.provider.Name, providerWidth))
		}
		line += m.latencyStyle(latency).Render(statsCell(nowText, nowWidth)) +
			title.Render(statsCell(p95Text, p95Width)) +
			muted.Render(statsCell(displayAge(ageText), ageWidth)) +
			m.latencyStyle(p95).Render(fitCells(graphLine, graphWidth))
		line = fitCells(line, contentWidth)
		if offset+rowIndex == m.sel[panelFavourites] && m.focused == panelFavourites {
			line = lipgloss.NewStyle().
				Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
				Background(lipgloss.Color(m.palette[theme.SelectedBg])).
				Bold(true).
				Render(line)
		} else if status == "unknown" {
			line = muted.Render(line)
		}
		lines = append(lines, line)
	}
	if len(lines) < h {
		if provider, model := m.selectedFavourite(); provider != nil && model != nil {
			lines = append(lines, title.Render(fitCells("selected  "+model.ID+" · "+provider.Name, contentWidth)))
		}
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", contentWidth))
	}
	for row := range h {
		lines[row] = fitCells(lines[row], contentWidth) +
			viewportScrollCell(len(favourites), offset, visibleRows, row, h, m.palette, theme.PaneFavourites)
	}
	return strings.Join(lines[:h], "\n")
}

// --- stats pane ---

// statsRow is one real provider/model row in the btop-style process table.
type statsRow struct {
	name, provider, kind, status string
	isFav                        bool
	checks, okPct, account, down int
	p50, p95, latency            float64
	age                          string
	ageUnix                      int64
	history                      []float64
}

func statsColumnsForWidth(width int) ([]table.Column, []string) {
	if width >= 80 {
		programWidth := max(12, width-68)
		return []table.Column{
			{Title: "slot", Width: 6},
			{Title: "provider/model", Width: programWidth},
			{Title: "provider", Width: 12},
			{Title: "●", Width: 2},
			{Title: "checks", Width: 7},
			{Title: "ok%", Width: 6},
			{Title: "account", Width: 8},
			{Title: "down", Width: 6},
			{Title: "p50", Width: 6},
			{Title: "p95", Width: 6},
			{Title: "age", Width: 9},
		}, []string{"slot", "name", "provider", "status", "checks", "ok%", "account", "down", "p50", "p95", "age"}
	}
	if width >= 55 {
		programWidth := max(12, width-30)
		return []table.Column{
			{Title: "●", Width: 2},
			{Title: "provider/model", Width: programWidth},
			{Title: "checks", Width: 7},
			{Title: "ok%", Width: 6},
			{Title: "p95", Width: 6},
			{Title: "age", Width: 9},
		}, []string{"status", "name", "checks", "ok%", "p95", "age"}
	}
	programWidth := max(8, width-17)
	return []table.Column{
		{Title: "●", Width: 2},
		{Title: "provider/model", Width: programWidth},
		{Title: "p95", Width: 6},
		{Title: "age", Width: 9},
	}, []string{"status", "name", "p95", "age"}
}

func statsColumnAt(x int, columns []table.Column) int {
	if x < 0 {
		return -1
	}
	position := 0
	for index, column := range columns {
		if x >= position && x < position+column.Width {
			return index
		}
		position += column.Width
	}
	return -1
}

func (m model) statsRows() []statsRow {
	var rows []statsRow
	for providerIndex := range m.cfg.Providers {
		providerConfig := &m.cfg.Providers[providerIndex]
		providerState := m.st.Providers[providerConfig.Name]
		rows = append(rows, m.statsRowFor(providerConfig, providerState))
		if !m.prefs.statsShowFavs {
			continue
		}
		for _, modelConfig := range providerConfig.Models {
			if !modelConfig.Favourite {
				continue
			}
			var modelState *state.ModelState
			if providerState != nil {
				modelState = providerState.Models[modelConfig.ID]
			}
			rows = append(rows, m.statsRowForModel(providerConfig.Name, modelConfig.ID, modelState))
		}
	}
	return sortStats(rows, m.prefs.statsSort, m.prefs.statsSortDir)
}

func (m model) statsRowFor(providerConfig *config.Provider, providerState *state.ProviderState) statsRow {
	providerLabel := providerConfig.Label
	if providerLabel == "" {
		providerLabel = providerConfig.Kind
	}
	row := statsRow{name: providerConfig.Name, provider: providerLabel, kind: "provider", status: "unknown"}
	if providerState == nil {
		return row
	}
	counter := providerState.Counters
	row.checks, row.account, row.down = counter.Checks, counter.Account, counter.Down
	row.p50, row.p95 = percentile(providerState.Ring, 50), percentile(providerState.Ring, 95)
	row.history = providerState.Ring
	if counter.Checks > 0 {
		row.okPct = counter.OK * 100 / counter.Checks
	}
	if providerState.LastCheck != nil {
		row.status = providerState.LastCheck.Status
		row.latency = providerState.LastCheck.LatencyMs
		row.age = state.RelAge(time.Since(providerState.LastCheck.CheckedAt))
		row.ageUnix = providerState.LastCheck.CheckedAt.Unix()
	}
	return row
}

func (m model) statsRowForModel(providerName, modelID string, modelState *state.ModelState) statsRow {
	row := statsRow{name: modelID, provider: providerName, kind: "fav", status: "unknown", isFav: true}
	if modelState == nil {
		return row
	}
	counter := modelState.Counters
	row.checks, row.account, row.down = counter.Checks, counter.Account, counter.Down
	row.p50, row.p95 = percentile(modelState.Ring, 50), percentile(modelState.Ring, 95)
	row.history = modelState.Ring
	if counter.Checks > 0 {
		row.okPct = counter.OK * 100 / counter.Checks
	}
	if modelState.LastCheck != nil {
		row.status = modelState.LastCheck.Status
		row.latency = modelState.LastCheck.LatencyMs
		row.age = state.RelAge(time.Since(modelState.LastCheck.CheckedAt))
		row.ageUnix = modelState.LastCheck.CheckedAt.Unix()
	}
	return row
}

// sortStats sorts rows by column key; dir 1=asc 2=desc 0=natural order.
// Returns a new slice; never mutates the input.
func sortStats(rows []statsRow, col string, dir int) []statsRow {
	if col == "" || dir == 0 {
		return rows
	}
	rows = append([]statsRow{}, rows...)
	key := func(row statsRow) float64 {
		switch col {
		case "checks":
			return float64(row.checks)
		case "ok%":
			return float64(row.okPct)
		case "p50":
			return row.p50
		case "p95", "history":
			return row.p95
		case "latency":
			return row.latency
		case "down":
			return float64(row.down)
		case "age":
			return float64(row.ageUnix)
		default:
			return 0
		}
	}
	text := func(row statsRow) string {
		switch col {
		case "provider":
			return row.provider
		case "kind":
			return row.kind
		case "status":
			return row.status
		default:
			return row.name
		}
	}
	less := func(a, b statsRow) bool {
		if col == "name" || col == "provider" || col == "kind" || col == "status" {
			if dir == 1 {
				return text(a) < text(b)
			}
			return text(a) > text(b)
		}
		if dir == 1 {
			return key(a) < key(b)
		}
		return key(a) > key(b)
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && less(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	return rows
}

// cycleStatsSort advances the sort state for a column: none→asc→desc→none.
func (m *model) cycleStatsSort(col string) {
	if m.prefs.statsSort == col {
		m.prefs.statsSortDir = (m.prefs.statsSortDir + 1) % 3
		if m.prefs.statsSortDir == 0 {
			m.prefs.statsSort = ""
		}
	} else {
		m.prefs.statsSort = col
		m.prefs.statsSortDir = 1
	}
	m.savePrefs()
}

func (m model) statsGraphMode(width int) bool {
	switch m.cfg.Settings.StatsMode {
	case "graph":
		return true
	case "table":
		return false
	default:
		return width < 70
	}
}

func (m model) renderStatsGraphPane(width, height int) string {
	rows := m.statsRows()
	if len(rows) == 0 {
		return ""
	}
	contentWidth := max(1, width-1)
	selected := min(max(0, m.sel[panelStats]), len(rows)-1)
	row := rows[selected]
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	muted := mutedStyle(m.palette)
	identity := fitCells(row.name+" · "+row.provider, contentWidth)
	if m.focused == panelStats {
		identity = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
			Background(lipgloss.Color(m.palette[theme.SelectedBg])).Bold(true).Render(identity)
	} else {
		identity = title.Render(identity)
	}
	age := displayAge(row.age)
	if age == "" {
		age = "—"
	}
	lines := []string{
		identity,
		muted.Render(fitCells(fmt.Sprintf("status %s · age %s · checks %d", row.status, age, row.checks), contentWidth)),
	}
	type metric struct {
		label, value string
		percent      float64
		role         theme.Role
	}
	accountPercent, downPercent := 0.0, 0.0
	if row.checks > 0 {
		accountPercent = float64(row.account) / float64(row.checks)
		downPercent = float64(row.down) / float64(row.checks)
	}
	metrics := []metric{
		{"success", fmt.Sprintf("%d%%", row.okPct), float64(row.okPct) / 100, theme.OK},
		{"p95 latency", fmt.Sprintf("%.0fms", row.p95), row.p95 / 600, theme.Warn},
		{"p50 latency", fmt.Sprintf("%.0fms", row.p50), row.p50 / 600, theme.BoxBorder},
		{"account", fmt.Sprintf("%d", row.account), accountPercent, theme.Warn},
		{"down", fmt.Sprintf("%d", row.down), downPercent, theme.Err},
	}
	blocks := max(1, (height-len(lines))/2)
	for _, metric := range metrics[:min(blocks, len(metrics))] {
		label := title.Render(metric.label)
		value := title.Render(metric.value)
		lines = append(lines,
			fitCells(label+strings.Repeat(" ", max(1, contentWidth-ansi.StringWidth(label)-ansi.StringWidth(value)))+value, contentWidth),
			m.btopMeterBar(clampUnit(metric.percent), contentWidth, metric.role),
		)
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", contentWidth))
	}
	return renderViewport(lines[:height], 0, height, width, m.palette, theme.PaneStats)
}

func (m model) btopMeterBar(percent float64, width int, role theme.Role) string {
	percent = clampUnit(percent)
	filled := int(percent*float64(width) + 0.5)
	fill := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[role])).Render(strings.Repeat("█", filled))
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.BarEmpty])).Render(strings.Repeat("█", width-filled))
	return fill + empty
}

func (m model) renderStatsPane(width, height int, _ bool) string {
	if m.statsGraphMode(width) {
		return m.renderStatsGraphPane(width, height)
	}
	rows := m.statsRows()
	if len(rows) == 0 {
		return ""
	}
	contentWidth := max(1, width-1)
	columns, keys := statsColumnsForWidth(contentWidth)
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	header := ""
	for index, column := range columns {
		label := column.Title
		if keys[index] == m.prefs.statsSort && m.prefs.statsSortDir != 0 {
			arrow := "↑"
			if m.prefs.statsSortDir == 2 {
				arrow = "↓"
			}
			label = ansi.Truncate(label, max(0, column.Width-1), "") + arrow
		}
		header += title.Render(statsCell(label, column.Width))
	}
	detailHeight := min(11, max(7, height/3))
	visible := max(1, height-2-detailHeight)
	offset := min(m.scroll[panelStats], max(0, len(rows)-visible))
	end := min(len(rows), offset+visible)
	lines := []string{fitCells(header, contentWidth) + m.statsScrollCell(len(rows), offset, visible, 0, height)}
	for index, row := range rows[offset:end] {
		line := m.renderStatsRow(row, offset+index+1, columns, keys, contentWidth)
		if offset+index == m.sel[panelStats] && m.focused == panelStats {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
				Background(lipgloss.Color(m.palette[theme.SelectedBg])).Bold(true).
				Render(fitCells(ansi.Strip(line), contentWidth))
		}
		lines = append(lines, fitCells(line, contentWidth)+m.statsScrollCell(len(rows), offset, visible, index+1, height))
	}
	selected := min(max(0, m.sel[panelStats]), len(rows)-1)
	for _, detail := range m.statsSelectedMeterDetails(rows[selected], contentWidth, detailHeight) {
		if len(lines) >= height-1 {
			break
		}
		lines = append(lines, fitCells(detail, contentWidth)+m.statsScrollCell(len(rows), offset, visible, len(lines), height))
	}
	for len(lines) < height-1 {
		row := len(lines)
		lines = append(lines, strings.Repeat(" ", contentWidth)+m.statsScrollCell(len(rows), offset, visible, row, height))
	}
	location := fmt.Sprintf("%d/%d", end, len(rows))
	footer := strings.Repeat("─", max(0, contentWidth-ansi.StringWidth(location)-1)) + " " + location
	lines = append(lines, mutedStyle(m.palette).Render(fitCells(footer, contentWidth))+m.statsScrollCell(len(rows), offset, visible, height-1, height))
	return strings.Join(lines[:height], "\n")
}

func mutedStyle(palette theme.Palette) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(palette[theme.Muted]))
}

func (m model) renderStatsRow(row statsRow, slot int, columns []table.Column, keys []string, width int) string {
	p95, age := "—", "—"
	if row.p95 > 0 {
		p95 = fmt.Sprintf("%.0f", row.p95)
	}
	if row.age != "" {
		age = displayAge(row.age)
	}
	statusColorValue := statusColor(m.palette, row.status)
	if row.status == "unknown" {
		statusColorValue = m.palette[theme.Title]
	}
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(statusColorValue)).Bold(true)
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	muted := mutedStyle(m.palette)
	styled := map[string]string{
		"slot": muted.Render(fmt.Sprintf("%d", slot)), "name": statusStyle.Render(row.name),
		"provider": muted.Render(row.provider), "status": m.glyphFor(row.status),
		"checks":  title.Render(fmt.Sprintf("%d", row.checks)),
		"ok%":     m.reliabilityStyle(row.okPct).Render(fmt.Sprintf("%d%%", row.okPct)),
		"account": muted.Render(fmt.Sprintf("%d", row.account)), "down": muted.Render(fmt.Sprintf("%d", row.down)),
		"p50": title.Render(fmt.Sprintf("%.0f", row.p50)), "p95": title.Render(p95), "age": muted.Render(age),
	}
	var line string
	for index, column := range columns {
		line += statsCell(styled[keys[index]], column.Width)
	}
	return fitCells(line, width)
}

func (m model) statsSelectedMeterDetails(row statsRow, width, maxLines int) []string {
	muted := mutedStyle(m.palette)
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	age := displayAge(row.age)
	if age == "" {
		age = "—"
	}
	errors := m.statsErrorsForRow(row)
	lines := []string{
		muted.Render(strings.Repeat("─", max(0, width))),
		title.Render(fitCells("selected  "+row.name+" · "+row.provider, width)),
		muted.Render(fitCells(fmt.Sprintf("status %s · age %s · checks %d · errors %d", row.status, age, row.checks, len(errors)), width)),
	}
	type metric struct {
		label, value string
		percent      float64
		role         theme.Role
	}
	accountPercent, downPercent := 0.0, 0.0
	if row.checks > 0 {
		accountPercent = float64(row.account) / float64(row.checks)
		downPercent = float64(row.down) / float64(row.checks)
	}
	metrics := []metric{
		{"success", fmt.Sprintf("%d%%", row.okPct), float64(row.okPct) / 100, theme.OK},
		{"p95 latency", fmt.Sprintf("%.0fms", row.p95), row.p95 / 600, theme.Warn},
		{"p50 latency", fmt.Sprintf("%.0fms", row.p50), row.p50 / 600, theme.BoxBorder},
		{"account", fmt.Sprintf("%d", row.account), accountPercent, theme.Warn},
		{"down", fmt.Sprintf("%d", row.down), downPercent, theme.Err},
	}
	metricLimit := maxLines
	if len(errors) > 0 {
		metricLimit--
	}
	for _, metric := range metrics {
		if len(lines)+2 > metricLimit {
			break
		}
		label, value := title.Render(metric.label), title.Render(metric.value)
		lines = append(lines,
			fitCells(label+strings.Repeat(" ", max(1, width-ansi.StringWidth(label)-ansi.StringWidth(value)))+value, width),
			m.btopMeterBar(clampUnit(metric.percent), width, metric.role),
		)
	}
	if len(errors) > 0 && len(lines) < maxLines {
		recent := errors[len(errors)-1]
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Err])).Render(
			fitCells(row.name+" · "+recent.Status+" · "+recent.Reason, width)))
	}
	return lines
}

func (m model) statsErrorsForRow(row statsRow) []state.ErrorEntry {
	if row.isFav {
		if providerState := m.st.Providers[row.provider]; providerState != nil {
			if modelState := providerState.Models[row.name]; modelState != nil {
				return modelState.RecentErrors
			}
		}
	} else if providerState := m.st.Providers[row.name]; providerState != nil {
		return providerState.RecentErrors
	}
	return nil
}

func statsCell(value string, width int) string {
	if width <= 1 {
		return fitCells(value, width)
	}
	return fitCells(value, width-1) + " "
}

func (m model) statsScrollCell(total, offset, visible, row, height int) string {
	inactive := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(panelStats)))
	if height < 1 || total <= visible {
		return inactive.Render("│")
	}
	active := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	thumbSize := max(1, height*visible/total)
	thumbStart := (height - thumbSize) * offset / max(1, total-visible)
	if row >= thumbStart && row < thumbStart+thumbSize {
		return active.Render("█")
	}
	return inactive.Render("│")
}

func (m model) usageLineCount() int {
	total := 0
	for _, providerConfig := range m.cfg.Providers {
		total++
		if providerConfig.Note != "" &&
			!strings.HasPrefix(providerConfig.Note, "configured from OMP") &&
			!strings.HasPrefix(providerConfig.Note, "OMP-managed") {
			total++
		}
		if len(providerConfig.Meters) == 0 {
			if providerState := m.st.Providers[providerConfig.Name]; providerState != nil && providerState.Counters.Checks > 0 {
				total += 3
			} else {
				total++
			}
			continue
		}
		for _, meter := range providerConfig.Meters {
			total += 2
			if meter.Auto != "" && m.st.GetMeter(providerConfig.Name, meter.Name) == nil {
				continue
			}
			if meter.Cap > 0 {
				total++
			}
		}
		if providerState := m.st.Providers[providerConfig.Name]; providerState != nil && providerState.Counters.Checks > 0 {
			total++
		}
	}
	return total
}

func renderViewport(lines []string, offset, height, width int, palette theme.Palette, role theme.Role) string {
	if height < 1 {
		height = 1
	}
	contentWidth := max(1, width-1)
	visible := height
	offset = min(max(0, offset), max(0, len(lines)-visible))
	end := min(len(lines), offset+visible)
	out := make([]string, height)
	for row := range height {
		line := ""
		if offset+row < end {
			line = lines[offset+row]
		}
		out[row] = fitCells(line, contentWidth) +
			viewportScrollCell(len(lines), offset, visible, row, height, palette, role)
	}
	return strings.Join(out, "\n")
}

func viewportScrollCell(total, offset, visible, row, height int, palette theme.Palette, role theme.Role) string {
	inactive := lipgloss.NewStyle().Foreground(lipgloss.Color(palette[role]))
	if height < 1 || total <= visible {
		return inactive.Render("│")
	}
	active := lipgloss.NewStyle().Foreground(lipgloss.Color(palette[theme.Title])).Bold(true)
	thumbSize := max(1, height*visible/total)
	thumbStart := (height - thumbSize) * offset / max(1, total-visible)
	if row >= thumbStart && row < thumbStart+thumbSize {
		return active.Render("█")
	}
	return inactive.Render("│")
}

// percentile returns the p-th percentile of ring.
func percentile(ring []float64, p int) float64 {
	if len(ring) == 0 {
		return 0
	}
	sorted := make([]float64, len(ring))
	copy(sorted, ring)
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

// renderScrollable renders a window of lines with a scrollbar when overflowing.
func renderScrollable(lines []string, offset, h int, pal theme.Palette) string {
	if h < 1 {
		h = 1
	}
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
	visible := append([]string{}, lines[offset:end]...)
	for len(visible) < h {
		visible = append(visible, "")
	}
	// Scrollbar when content overflows: pad every line to the widest first,
	// so the track forms a straight column at the pane edge.
	if len(lines) > h {
		bar := scrollbar(len(lines), offset, h, pal)
		maxW := 0
		for _, l := range visible {
			if lw := lipgloss.Width(l); lw > maxW {
				maxW = lw
			}
		}
		for i := range visible {
			if pad := maxW - lipgloss.Width(visible[i]); pad > 0 {
				visible[i] += strings.Repeat(" ", pad)
			}
			visible[i] += " " + bar[i]
		}
	}
	return strings.Join(visible, "\n")
}

// scrollbar renders a block-glyph scrollbar column of height h.
func scrollbar(total, offset, h int, pal theme.Palette) []string {
	out := make([]string, h)
	fill := lipgloss.NewStyle().Foreground(lipgloss.Color(pal[theme.BarFill])).Render("█")
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color(pal[theme.BarEmpty])).Render("░")
	// Thumb size proportional to visible fraction.
	thumb := h * h / total
	if thumb < 1 {
		thumb = 1
	}
	maxOff := total - h
	pos := 0
	if maxOff > 0 {
		pos = (h - thumb) * offset / maxOff
	}
	for i := range out {
		if i >= pos && i < pos+thumb {
			out[i] = fill
		} else {
			out[i] = empty
		}
	}
	return out
}
