package dashboard

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// handleClick processes a mouse click: focus pane, select row, or activate
// a heading/button region.
func (m model) handleClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	x, y := mouse.X, mouse.Y

	// Header buttons.
	for _, r := range m.regions {
		if !inRegion(x, y, r) {
			continue
		}
		switch r.kind {
		case "check-button":
			if !m.checking {
				cmd := m.startCheckAll()
				return m, cmd
			}
			return m, nil
		case "cycle-view":
			return m.cycleView(), nil
		case "theme":
			return m.cycleTheme(), nil
		case "settings":
			m.ov = m.newSettingsOverlay()
			if m.ov.kind == overlayForm {
				return m, m.ov.form.Init()
			}
			return m, nil
		case "help":
			m.ov = m.newHelpOverlay()
			return m, nil
		case "menu":
			m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: []menuItem{
				{"add provider", "main.add"},
				{"settings", "main.settings"},
				{"cycle theme", "main.theme"},
				{"cycle view", "main.view"},
				{"help", "main.help"},
				{"quit", "main.quit"},
			}}
			return m, nil
		}
	}

	// Overlay: clicks inside menus select/activate; elsewhere dismiss.
	if m.ov.kind == overlayMenu {
		return m.menuClick(x, y)
	}
	if m.ov.kind == overlayConfirm {
		m.ov = overlayState{}
		return m, nil
	}

	// Pane rows: find which pane contains (x, y) by replaying layout geometry.
	p, localRow, paneX, ok := m.panelAt(x, y)
	if !ok {
		return m, nil
	}
	m.focused = p
	// Stats header row click → cycle sort on that column.
	if p == panelStats && localRow == 0 {
		// Compute the pane's content width to resolve the responsive column set.
		paneW := m.width - paneX - 4 // borders + padding allowance
		cols, keys := statsColumnsForWidth(paneW)
		col := statsColumnAt(x-paneX-1, cols) // -1 for border
		if col >= 0 && col < len(keys) {
			m.cycleStatsSort(keys[col])
		}
		return m, nil
	}
	if localRow >= 1 || (p != panelStats && localRow >= 0) {
		// Content rows: stats table has header at row 0, data from row 1.
		row := localRow
		if p == panelStats {
			row = localRow - 1
		}
		m.sel[p] = m.scroll[p] + row
		if max := m.maxSelection(p); m.sel[p] > max && max >= 0 {
			m.sel[p] = max
		}
	}
	// Heading clicks: top border row of the pane opens the pane menu.
	if localRow == -1 {
		switch p {
		case panelStatus:
			m.ov = m.newMenuOverlay(panelStatus)
		case panelUsage:
			m.ov = m.newMenuOverlay(panelUsage)
		case panelFavourites:
			m.ov = m.newMenuOverlay(panelFavourites)
		case panelStats:
			m.ov = m.newMenuOverlay(panelStats)
		}
	}
	return m, nil
}

// --- pane hit testing continues below ---

// handleWheel scrolls the pane under the cursor.
func (m model) handleWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if m.ov.kind == overlayViewport {
		var cmd tea.Cmd
		m.ov.vp, cmd = m.ov.vp.Update(tea.MouseWheelMsg(mouse))
		return m, cmd
	}
	p, _, _, ok := m.panelAt(mouse.X, mouse.Y)
	if !ok {
		return m, nil
	}
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.scroll[p]--
		if m.scroll[p] < 0 {
			m.scroll[p] = 0
		}
	case tea.MouseWheelDown:
		m.scroll[p]++
		maxScroll := m.maxScroll(p)
		if m.scroll[p] > maxScroll {
			m.scroll[p] = maxScroll
		}
	}
	return m, nil
}

// maxScroll computes the largest valid scroll offset for a pane.
func (m model) maxScroll(p panelID) int {
	var total int
	switch p {
	case panelStatus:
		total = len(m.sortedProviders())
	case panelFavourites:
		total = len(m.favouriteList())
	case panelUsage:
		// Count rendered lines once.
		content := m.renderUsagePane(80, 1<<20, false)
		total = strings.Count(content, "\n") + 1
	case panelStats:
		total = len(m.statsRows()) + 1
	}
	max := total - m.paneContentHeight()
	if max < 0 {
		max = 0
	}
	return max
}

// menuClick maps a click to a menu row when the menu overlay is open.
func (m model) menuClick(x, y int) (tea.Model, tea.Cmd) {
	// Menu overlay is centered; recompute its geometry.
	items := len(m.ov.menuItems)
	ovH := items + 4 // padding + hint line
	ovW := 0
	for _, it := range m.ov.menuItems {
		if len(it.label)+4 > ovW {
			ovW = len(it.label) + 4
		}
	}
	if ovW < 24 {
		ovW = 24
	}
	startX := (m.width - ovW) / 2
	startY := (m.height - ovH) / 2
	row := y - startY - 1 // top border
	if row >= 0 && row < items && x >= startX && x < startX+ovW {
		m.ov.menuSel = row
		return m.runMenuAction(m.ov.menuItems[row].action)
	}
	m.ov = overlayState{}
	return m, nil
}

// panelAt maps screen coordinates to a pane + local content row + pane origin x.
// Row -1 means the pane's top border (heading); row 0 is the first content row.
func (m model) panelAt(x, y int) (panelID, int, int, bool) {
	if y < 2 || y >= m.height-1 {
		return 0, 0, 0, false // header (2 lines) / footer
	}
	view := m.activeViewDef()
	stack := m.width < 100 || m.height < 24 || view.Arrangement == "stack" || m.zoomed

	if m.zoomed {
		return m.focused, y - 3, 0, true
	}
	if stack {
		panels := m.visiblePanels()
		avail := m.height - 3
		per := avail / len(panels)
		rel := y - 2
		idx := rel / per
		if idx >= len(panels) {
			idx = len(panels) - 1
		}
		local := rel - idx*per - 1 // -1 for border
		return panels[idx], local, 0, true
	}

	// Grid: round-robin columns exactly as renderGrid.
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

	col := left
	paneX := 0
	if x >= leftW && len(right) > 0 {
		col = right
		paneX = leftW
	}
	avail := m.height - 3
	per := avail / len(col)
	rel := y - 2
	idx := rel / per
	if idx >= len(col) {
		idx = len(col) - 1
	}
	local := rel - idx*per - 1
	return col[idx], local, paneX, true
}

func inRegion(x, y int, r hitRegion) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}
