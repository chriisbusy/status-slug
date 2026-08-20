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
	ok, account, down, unknown, fav string
}

// unicodeGlyphs is the default, terminal-safe set.
var unicodeGlyphs = glyphSet{ok: "●", account: "◐", down: "○", unknown: "◌", fav: "★"}

// nerdGlyphs is the nerd_font=true set (Nerd Font codepoints).
var nerdGlyphs = glyphSet{ok: "\uf00c", account: "\uf06a", down: "\uf00d", unknown: "\uf128", fav: "\uf005"}

// glyphs returns the active glyph set per settings.nerd_font.
func (m model) glyphs() glyphSet {
	if m.cfg.Settings.NerdFont {
		return nerdGlyphs
	}
	return unicodeGlyphs
}

func (m model) glyphFav() string { return m.glyphs().fav }

// emptyHint renders an actionable empty-state line: muted lead-in, accent key.
func (m model) emptyHint(lead, key, rest string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted])).Render(lead+" ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true).Render("["+key+"]") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted])).Render(rest)
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

func (m model) renderStatusPane(w, h int, compact bool) string {
	provs := m.sortedProviders()
	if len(provs) == 0 {
		return m.emptyHint("no providers —", "a", "dd your first one")
	}
	ok, account, down, unknown := m.healthCounts()
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	lines := []string{
		fmt.Sprintf("%s %s  %s  %s  %s  %s",
			dim.Render("fleet"),
			m.statusStrip(provs, w/3),
			m.statusPill("ok", ok),
			m.statusPill("account", account),
			m.statusPill("down", down),
			m.statusPill("unknown", unknown)),
		dim.Render("row = live endpoint card · reason line carries the why"),
	}
	if m.prefs.statusGroup {
		lines = append(lines, m.statusGrouped(provs, w, compact)...)
	} else {
		for i, p := range provs {
			lines = append(lines, m.statusRow(p, i == m.sel[panelStatus], w, compact)...)
		}
	}
	return renderScrollable(lines, m.scroll[panelStatus], h, m.palette)
}

func (m model) statusGrouped(provs []*config.Provider, w int, compact bool) []string {
	// Group by label preserving sorted order within groups.
	var order []string
	byLabel := map[string][]*config.Provider{}
	for _, p := range provs {
		l := p.Label
		if l == "" {
			l = "custom"
		}
		if _, ok := byLabel[l]; !ok {
			order = append(order, l)
		}
		byLabel[l] = append(byLabel[l], p)
	}
	var lines []string
	selIdx := 0
	for _, l := range order {
		hdr := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(panelStatus))).Bold(true).
			Render("╼ " + strings.ToUpper(l) + " ")
		lines = append(lines, hdr)
		for _, p := range byLabel[l] {
			lines = append(lines, m.statusRow(p, selIdx == m.sel[panelStatus], w, compact)...)
			selIdx++
		}
	}
	return lines
}

func (m model) statusRow(p *config.Provider, selected bool, w int, compact bool) []string {
	status := "unknown"
	latency := ""
	latencyMs := 0.0
	age := ""
	reason := ""
	if ps := m.st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
		lc := ps.LastCheck
		status = lc.Status
		reason = lc.Reason
		if lc.LatencyMs > 0 {
			latencyMs = lc.LatencyMs
			latency = fmt.Sprintf("%.0fms", lc.LatencyMs)
		}
		if !lc.CheckedAt.IsZero() {
			age = state.RelAge(time.Since(lc.CheckedAt))
		}
	}
	dot := m.probeGlyph(status)
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(statusColor(m.palette, status))).Bold(true)
	if !p.Enabled {
		status = "disabled"
		reason = "disabled · skipped by probes; usage meters still shown"
		dot = dim.Render("◌")
		nameStyle = dim
		statusStyle = dim
	}
	label := p.Label
	if label == "" {
		label = p.Kind
	}
	if label == "" {
		label = "custom"
	}
	labelChip := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
		Background(lipgloss.Color(m.panelChrome(panelStatus))).
		Render(" " + truncate(label, 10) + " ")
	meta := statusStyle.Render(status)
	if latency != "" {
		meta += dim.Render(" · ") + m.latencyStyle(latencyMs).Render(latency)
	}
	if age != "" {
		meta += dim.Render(" · " + age)
	}
	primary := fmt.Sprintf("%s %s %s  %s",
		dot,
		nameStyle.Render(truncate(p.Name, 20)),
		labelChip,
		meta)
	if selected {
		primary = lipgloss.NewStyle().
			Background(lipgloss.Color(m.palette[theme.SelectedBg])).
			Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
			Render("> " + primary)
	} else {
		primary = "  " + primary
	}
	lines := []string{primary}
	detail := statusReasonSummary(status, reason, p.Kind, p.BaseURL)
	if latencyMs > 0 && !compact {
		detail = "latency " + m.latencyBar(latencyMs, 10) + " · " + detail
	}
	if detail != "" && !compact {
		if w > 8 {
			detail = truncate(detail, w-6)
		}
		lines = append(lines, "    "+dim.Render("╰─ ")+detail)
	}
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

func (m model) statusPill(label string, count int) string {
	role := theme.Unknown
	switch label {
	case "ok":
		role = theme.OK
	case "account":
		role = theme.Warn
	case "down":
		role = theme.Err
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[role])).Bold(true)
	return st.Render(fmt.Sprintf("%s %d", label, count))
}

func (m model) statusStrip(provs []*config.Provider, maxCells int) string {
	if maxCells < 4 {
		maxCells = 4
	}
	var b strings.Builder
	for i, p := range provs {
		if i >= maxCells {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted])).Render("…"))
			break
		}
		status := "unknown"
		if !p.Enabled {
			status = "unknown"
		} else if ps := m.st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
			status = ps.LastCheck.Status
		}
		b.WriteString(m.glyphFor(status))
	}
	return b.String()
}

func (m model) latencyBar(ms float64, w int) string {
	if w <= 0 {
		return ""
	}
	pct := ms / 1000
	if pct > 1 {
		pct = 1
	}
	if pct < 0 {
		pct = 0
	}
	fillCells := int(pct*float64(w) + 0.5)
	if fillCells < 1 && ms > 0 {
		fillCells = 1
	}
	if fillCells > w {
		fillCells = w
	}
	fill := m.latencyStyle(ms).Render(strings.Repeat("▰", fillCells))
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.BarEmpty])).Render(strings.Repeat("▱", w-fillCells))
	return fill + empty
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

func (m model) renderUsagePane(w, h int, compact bool) string {
	if len(m.cfg.Providers) == 0 {
		return m.emptyHint("no providers —", "a", "dd your first one")
	}
	total, auto, _ := m.meterCounts()
	manual := total - auto
	uncapped := 0
	capped := 0
	capPctSum := 0.0
	for _, p := range m.cfg.Providers {
		for _, meter := range p.Meters {
			if meter.Cap <= 0 {
				uncapped++
				continue
			}
			capped++
			val := meter.Used
			if mv := m.st.GetMeter(p.Name, meter.Name); mv != nil {
				val = mv.Value
			}
			pct := val / meter.Cap
			if pct < 0 {
				pct = 0
			}
			if pct > 1 {
				pct = 1
			}
			capPctSum += pct
		}
	}
	capAvg := 0.0
	if capped > 0 {
		capAvg = capPctSum / float64(capped)
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	lines := []string{
		fmt.Sprintf("%s %s %s",
			dim.Render("cap avg"),
			m.meterBar(capAvg, 14),
			dim.Render(fmt.Sprintf("%3.0f%% across %d capped", capAvg*100, capped))),
		fmt.Sprintf("%s %s  %s  %s  %s",
			dim.Render("meters"),
			m.usagePill("total", total),
			m.usagePill("manual", manual),
			m.usagePill("auto", auto),
			m.usagePill("uncapped", uncapped)),
	}
	for _, p := range m.sortedProviders() {
		dot := m.glyphFor("unknown")
		status := "unknown"
		if ps := m.st.Providers[p.Name]; ps != nil && ps.LastCheck != nil {
			status = ps.LastCheck.Status
			dot = m.glyphFor(status)
		}
		hdr := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(panelUsage))).Bold(true)
		if len(p.Meters) == 0 {
			lines = append(lines, fmt.Sprintf("  %s %-18s %s", dot, hdr.Render(truncate(p.Name, 18)), dim.Render("no meters · "+status)))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s %s %s", dot, hdr.Render(p.Name), dim.Render(status)))
		if p.Note != "" && !compact {
			lines = append(lines, "    "+dim.Render(p.Note))
		}
		for _, meter := range p.Meters {
			lines = append(lines, m.meterLines(p.Name, meter, w, compact)...)
		}
		if ps := m.st.Providers[p.Name]; ps != nil && ps.Counters.Checks > 0 && !compact {
			c := ps.Counters
			lines = append(lines, "    "+dim.Render(fmt.Sprintf("probes %d ok · %d account · %d down", c.OK, c.Account, c.Down)))
		}
		if !compact {
			lines = append(lines, "")
		}
	}
	return renderScrollable(lines, m.scroll[panelUsage], h, m.palette)
}

// meterLines renders one meter: value line, optional bar, reset line.
func (m model) meterLines(provName string, meter config.Meter, w int, compact bool) []string {
	mv := m.st.GetMeter(provName, meter.Name)
	val := meter.Used
	var setAt time.Time
	if mv != nil {
		val = mv.Value
		setAt = mv.SetAt
	}
	pct := 0.0
	if meter.Cap > 0 {
		pct = val / meter.Cap
		if pct > 1 {
			pct = 1
		}
		if pct < 0 {
			pct = 0
		}
	}
	name := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true).Render(meter.Name)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	source := meter.Kind
	if source == "" {
		source = "manual"
	}
	line := fmt.Sprintf("    %s  %.4g", name, val)
	if meter.Cap > 0 {
		line += fmt.Sprintf(" / %.4g", meter.Cap)
	} else {
		line += " / uncapped"
	}
	line += " " + meter.Unit + dim.Render(" · "+source)
	if meter.Kind == "auto" && meter.Auto != "" {
		line += dim.Render(":" + meter.Auto)
	}
	lines := []string{line}
	if meter.Cap > 0 && w > 14 {
		barW := w - 14
		if barW > 46 {
			barW = 46
		}
		if barW < 10 {
			barW = 10
		}
		lines = append(lines, "    "+m.meterBar(pct, barW)+" "+dim.Render(fmt.Sprintf("%3.0f%%", pct*100)))
	}
	var meta []string
	if meter.Reset != "" && meter.Reset != "never" {
		if rl, urgency := resetDescription(meter.Reset, time.Now()); rl != "" {
			c := m.palette[theme.Muted]
			switch urgency {
			case -1:
				c = m.palette[theme.Err]
			case 1:
				c = m.palette[theme.Warn]
			}
			meta = append(meta, lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(rl))
		}
	}
	if !setAt.IsZero() {
		meta = append(meta, dim.Render("set "+state.RelAge(time.Since(setAt))))
	}
	if len(meta) > 0 && !compact {
		lines = append(lines, "    "+strings.Join(meta, dim.Render(" · ")))
	}
	return lines
}

func (m model) usagePill(label string, count int) string {
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(panelUsage))).Bold(true)
	if label == "uncapped" && count > 0 {
		st = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Warn])).Bold(true)
	}
	return st.Render(fmt.Sprintf("%s %d", label, count))
}

func (m model) meterBar(pct float64, w int) string {
	if w <= 0 {
		return ""
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 1 {
		pct = 1
	}
	fillCells := int(pct*float64(w) + 0.5)
	if fillCells > w {
		fillCells = w
	}
	fillColor := m.palette[theme.OK]
	switch {
	case pct >= 0.85:
		fillColor = m.palette[theme.Err]
	case pct >= 0.60:
		fillColor = m.palette[theme.Warn]
	}
	fill := lipgloss.NewStyle().Foreground(lipgloss.Color(fillColor)).Render(strings.Repeat("█", fillCells))
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.BarEmpty])).Render(strings.Repeat("░", w-fillCells))
	return fill + empty
}

// resetDescription renders a human reset line and its urgency:
// -1 overdue, 1 imminent (<2 days), 0 otherwise.
func resetDescription(spec string, now time.Time) (string, int) {
	next := provider.NextResetForTest(spec, now)
	d := time.Until(next)
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

func (m model) renderFavouritesPane(w, h int, compact bool) string {
	favs := m.favouriteList()
	if len(favs) == 0 {
		return m.emptyHint("no favourites —", "f", " menu to add one")
	}
	sparkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.SparkHi]))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	healthy, maxP95 := m.favouriteHealth(favs)
	nameW, providerW := favouriteColumnWidths(w)
	header := fmt.Sprintf("%-*s %-*s %5s %5s %s", nameW, "model", providerW, "provider", "now", "p95", "latency tape")
	lines := []string{
		fmt.Sprintf("%s %s  %s  %s",
			dim.Render("cockpit"),
			m.favouriteStrip(favs, w/3),
			m.usagePill("watched", len(favs)),
			dim.Render(fmt.Sprintf("%d live · max p95 %.0fms", healthy, maxP95))),
		dim.Render(truncate(header, w)),
	}
	for i, f := range favs {
		name := f.model.ID
		if f.model.Alias != "" {
			name = f.model.Alias
		}
		name = truncate(name, nameW)
		status := "unknown"
		latency := ""
		latencyMs := 0.0
		age := ""
		var ring []float64
		if ps := m.st.Providers[f.provider.Name]; ps != nil {
			if ms := ps.Models[f.model.ID]; ms != nil {
				ring = ms.Ring
				if ms.LastCheck != nil {
					status = ms.LastCheck.Status
					if ms.LastCheck.LatencyMs > 0 {
						latencyMs = ms.LastCheck.LatencyMs
						latency = fmt.Sprintf("%.0fms", ms.LastCheck.LatencyMs)
					}
					age = state.RelAge(time.Since(ms.LastCheck.CheckedAt))
				}
			}
		}
		p95 := percentile(ring, 95)
		favMark := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(panelFavourites))).Render(m.glyphFav())
		providerChip := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
			Background(lipgloss.Color(m.panelChrome(panelFavourites))).
			Render(" " + truncate(f.provider.Name, providerW-2) + " ")
		prefix := fmt.Sprintf("%s %-*s %-*s %s %7s %5.0f ",
			favMark,
			nameW,
			name,
			providerW,
			providerChip,
			m.probeGlyph(status),
			m.latencyStyle(latencyMs).Render(latency),
			p95)
		sparkW := w - 2 - ansi.StringWidth(prefix)
		if sparkW > 30 {
			sparkW = 30
		}
		if sparkW < 4 {
			sparkW = 4
		}
		line := prefix + sparkStyle.Render(Spark(ring, sparkW, m.cfg.Settings.GraphGlyphs))
		if i == m.sel[panelFavourites] && m.focused == panelFavourites {
			line = lipgloss.NewStyle().
				Background(lipgloss.Color(m.palette[theme.SelectedBg])).
				Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
				Render("> " + line)
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
		if !compact {
			detail := "    " + f.provider.Kind + " · " + status + " · " + age + " · tail " + m.cfg.Settings.GraphGlyphs
			lines = append(lines, dim.Render(truncate(detail, w)))
		}
	}
	return renderScrollable(lines, m.scroll[panelFavourites], h, m.palette)
}

func favouriteColumnWidths(w int) (nameW, providerW int) {
	nameW, providerW = 20, 13
	for nameW+providerW+31 > w && nameW > 10 {
		nameW--
	}
	for nameW+providerW+31 > w && providerW > 9 {
		providerW--
	}
	return nameW, providerW
}

func (m model) favouriteHealth(favs []favouriteEntry) (healthy int, maxP95 float64) {
	for _, f := range favs {
		if ps := m.st.Providers[f.provider.Name]; ps != nil {
			if ms := ps.Models[f.model.ID]; ms != nil {
				if ms.LastCheck != nil && ms.LastCheck.Status == "ok" {
					healthy++
				}
				if p95 := percentile(ms.Ring, 95); p95 > maxP95 {
					maxP95 = p95
				}
			}
		}
	}
	return healthy, maxP95
}

func (m model) favouriteStrip(favs []favouriteEntry, maxCells int) string {
	if maxCells < 4 {
		maxCells = 4
	}
	var b strings.Builder
	for i, f := range favs {
		if i >= maxCells {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted])).Render("…"))
			break
		}
		status := "unknown"
		if ps := m.st.Providers[f.provider.Name]; ps != nil {
			if ms := ps.Models[f.model.ID]; ms != nil && ms.LastCheck != nil {
				status = ms.LastCheck.Status
			}
		}
		b.WriteString(m.glyphFor(status))
	}
	return b.String()
}

// --- stats pane ---

// statsRow is one rendered row's backing data for sorting.
type statsRow struct {
	name     string
	isFav    bool
	checks   int
	okPct    int
	p50, p95 float64
	down     int
	age      string
	ageUnix  int64
}

// statsColumns are the table columns at full width; header click cycles sort.
var statsColumns = []table.Column{
	{Title: "name", Width: 14},
	{Title: "chk", Width: 4},
	{Title: "ok", Width: 4},
	{Title: "p50", Width: 5},
	{Title: "p95", Width: 5},
	{Title: "↓", Width: 3},
	{Title: "ago", Width: 6},
}

// statsColKeys maps column index to sort key (aligned with statsColumns).
var statsColKeys = []string{"name", "checks", "ok%", "p50", "p95", "down", "ago"}

// statsColumnsForWidth returns the column set that fits w. p50/p95/down are
// core promises of the stats pane, so narrow layouts drop age and checks first
// and then shrink the name column rather than hiding latency percentiles.
func statsColumnsForWidth(w int) ([]table.Column, []string) {
	cols := append([]table.Column{}, statsColumns...)
	keys := append([]string{}, statsColKeys...)
	total := 0
	for _, c := range cols {
		total += c.Width + 1
	}
	dropOrder := []string{"ago", "checks"}
	for _, drop := range dropOrder {
		if total <= w {
			break
		}
		for i, k := range keys {
			if k == drop {
				total -= cols[i].Width + 1
				cols = append(cols[:i], cols[i+1:]...)
				keys = append(keys[:i], keys[i+1:]...)
				break
			}
		}
	}
	if total > w && len(cols) > 0 {
		excess := total - w
		cols[0].Width -= excess
		if cols[0].Width < 6 {
			cols[0].Width = 6
		}
	}
	return cols, keys
}

// statsColumnAt maps a content-x coordinate to a column index in cols, or -1.
func statsColumnAt(x int, cols []table.Column) int {
	if x < 0 {
		return -1
	}
	pos := 0
	for i, c := range cols {
		if x >= pos && x < pos+c.Width {
			return i
		}
		pos += c.Width + 1
	}
	return -1
}

func compactStatsNameWidth(w int) int {
	nameW := w - 18
	if nameW > 12 {
		nameW = 12
	}
	if nameW < 7 {
		nameW = 7
	}
	return nameW
}

func compactStatsColumnAt(x, w int) (string, bool) {
	if x < 0 {
		return "", false
	}
	nameW := compactStatsNameWidth(w)
	ranges := []struct {
		key        string
		start, end int
	}{
		{"name", 0, nameW},
		{"ok%", nameW + 1, nameW + 4},
		{"p50", nameW + 5, nameW + 8},
		{"p95", nameW + 9, nameW + 12},
		{"down", nameW + 13, nameW + 14},
	}
	for _, r := range ranges {
		if x >= r.start && x < r.end {
			return r.key, true
		}
	}
	return "", false
}

func (m model) statsRows() []statsRow {
	var rows []statsRow
	for i := range m.cfg.Providers {
		pv := &m.cfg.Providers[i]
		ps := m.st.Providers[pv.Name]
		if ps == nil || ps.Counters.Checks == 0 {
			continue
		}
		rows = append(rows, m.statsRowFor(pv.Name, ps, false))
		if m.prefs.statsShowFavs {
			for _, mod := range pv.Models {
				if !mod.Favourite {
					continue
				}
				if ms := ps.Models[mod.ID]; ms != nil && ms.Counters.Checks > 0 {
					rows = append(rows, m.statsRowForModel(mod.ID, ms))
				}
			}
		}
	}
	return sortStats(rows, m.prefs.statsSort, m.prefs.statsSortDir)
}

func (m model) statsRowFor(name string, ps *state.ProviderState, _ bool) statsRow {
	c := ps.Counters
	r := statsRow{
		name:   name,
		checks: c.Checks,
		down:   c.Down,
		p50:    percentile(ps.Ring, 50),
		p95:    percentile(ps.Ring, 95),
	}
	if c.Checks > 0 {
		r.okPct = c.OK * 100 / c.Checks
	}
	if ps.LastCheck != nil {
		r.age = state.RelAge(time.Since(ps.LastCheck.CheckedAt))
		r.ageUnix = ps.LastCheck.CheckedAt.Unix()
	}
	return r
}

func (m model) statsRowForModel(id string, ms *state.ModelState) statsRow {
	c := ms.Counters
	r := statsRow{
		name:   "  ☆ " + id,
		isFav:  true,
		checks: c.Checks,
		down:   c.Down,
		p50:    percentile(ms.Ring, 50),
		p95:    percentile(ms.Ring, 95),
	}
	if c.Checks > 0 {
		r.okPct = c.OK * 100 / c.Checks
	}
	if ms.LastCheck != nil {
		r.age = state.RelAge(time.Since(ms.LastCheck.CheckedAt))
		r.ageUnix = ms.LastCheck.CheckedAt.Unix()
	}
	return r
}

// sortStats sorts rows by column key; dir 1=asc 2=desc 0=natural order.
// Returns a new slice; never mutates the input.
func sortStats(rows []statsRow, col string, dir int) []statsRow {
	if col == "" || dir == 0 {
		return rows
	}
	rows = append([]statsRow{}, rows...)
	key := func(r statsRow) float64 {
		switch col {
		case "checks":
			return float64(r.checks)
		case "ok%":
			return float64(r.okPct)
		case "p50":
			return r.p50
		case "p95":
			return r.p95
		case "down":
			return float64(r.down)
		case "ago":
			return float64(r.ageUnix)
		default:
			return 0
		}
	}
	less := func(a, b statsRow) bool {
		if col == "name" {
			if dir == 1 {
				return a.name < b.name
			}
			return a.name > b.name
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

func (m model) sortedColumns(cols []table.Column, keys []string) []table.Column {
	out := append([]table.Column{}, cols...)
	for i, k := range keys {
		if k != m.prefs.statsSort {
			continue
		}
		arrow := "↑"
		if m.prefs.statsSortDir == 2 {
			arrow = "↓"
		}
		title := ""
		if out[i].Width > 1 {
			title = truncate(out[i].Title, out[i].Width-1)
		}
		out[i].Title = title + arrow
	}
	return out
}

// statsTable builds the bubbles/table model for the stats pane.
func (m model) statsTable(w, h int) (table.Model, bool) {
	rows := m.statsRows()
	if len(rows) == 0 {
		return table.Model{}, false
	}
	cols, keys := statsColumnsForWidth(w)

	trs := make([]table.Row, len(rows))
	for i, r := range rows {
		// ok% colored by health like btop's meters: ≥95 green, ≥70 amber, else red.
		okPctStr := fmt.Sprintf("%d%%", r.okPct)
		if !theme.IsMono(m.palette) {
			c := m.palette[theme.Err]
			if r.okPct >= 95 {
				c = m.palette[theme.OK]
			} else if r.okPct >= 70 {
				c = m.palette[theme.Warn]
			}
			okPctStr = lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(okPctStr)
		}
		// Build the row in full-width key order, then project onto the
		// responsive column set.
		full := map[string]string{
			"name":   truncate(r.name, cols[0].Width),
			"checks": fmt.Sprintf("%d", r.checks),
			"ok%":    okPctStr,
			"p50":    fmt.Sprintf("%.0f", r.p50),
			"p95":    fmt.Sprintf("%.0f", r.p95),
			"down":   fmt.Sprintf("%d", r.down),
			"ago":    r.age,
		}
		row := make(table.Row, len(keys))
		for j, k := range keys {
			row[j] = full[k]
		}
		trs[i] = row
	}

	styles := table.DefaultStyles()
	styles.Header = styles.Header.
		Foreground(lipgloss.Color(m.panelChrome(panelStats))).
		Bold(true)
	styles.Selected = styles.Selected.
		Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
		Background(lipgloss.Color(m.palette[theme.SelectedBg]))
	styles.Cell = styles.Cell.
		Foreground(lipgloss.Color(m.palette[theme.Fg]))

	t := table.New(
		table.WithColumns(m.sortedColumns(cols, keys)),
		table.WithRows(trs),
		table.WithFocused(m.focused == panelStats),
		table.WithHeight(h-1),
		table.WithWidth(w),
		table.WithStyles(styles),
	)
	// Sync table cursor with our selection.
	if m.sel[panelStats] < len(trs) {
		t.SetCursor(m.sel[panelStats])
	}
	return t, true
}

func (m model) renderStatsPane(w, h int, compact bool) string {
	rows := m.statsRows()
	if len(rows) == 0 {
		return m.emptyHint("no data yet —", "c", "heck to start measuring")
	}
	if w < 50 {
		return m.renderStatsCompact(rows, w, h)
	}
	t, ok := m.statsTable(w, h)
	if !ok {
		return m.emptyHint("no data yet —", "c", "heck to start measuring")
	}
	return t.View()
}

func (m model) renderStatsCompact(rows []statsRow, w, h int) string {
	chrome := lipgloss.NewStyle().Foreground(lipgloss.Color(m.panelChrome(panelStats))).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	nameW := compactStatsNameWidth(w)
	totalChecks, totalDown, okSum := 0, 0, 0
	for _, r := range rows {
		totalChecks += r.checks
		totalDown += r.down
		okSum += r.okPct
	}
	avgOK := 0
	if len(rows) > 0 {
		avgOK = okSum / len(rows)
	}
	header := fmt.Sprintf("%-*s %3s %3s %3s %s", nameW, "name", "ok", "p50", "p95", "↓")
	lines := []string{
		fmt.Sprintf("%s %s  %s  %s",
			dim.Render("stats"),
			chrome.Render(fmt.Sprintf("%d checks", totalChecks)),
			dim.Render(fmt.Sprintf("avg ok %d%%", avgOK)),
			dim.Render(fmt.Sprintf("down %d", totalDown))),
		chrome.Render(header),
	}
	for i, r := range rows {
		okPct := fmt.Sprintf("%d%%", r.okPct)
		if !theme.IsMono(m.palette) {
			c := m.palette[theme.Err]
			if r.okPct >= 95 {
				c = m.palette[theme.OK]
			} else if r.okPct >= 70 {
				c = m.palette[theme.Warn]
			}
			okPct = lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(okPct)
		}
		line := fmt.Sprintf("%-*s %3s %3.0f %3.0f %d", nameW, truncate(r.name, nameW), okPct, r.p50, r.p95, r.down)
		if i == m.sel[panelStats] && m.focused == panelStats {
			line = lipgloss.NewStyle().
				Background(lipgloss.Color(m.palette[theme.SelectedBg])).
				Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
				Render(line)
		} else if r.isFav {
			line = dim.Render(line)
		}
		lines = append(lines, line)
	}
	return renderScrollable(lines, m.scroll[panelStats], h, m.palette)
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
