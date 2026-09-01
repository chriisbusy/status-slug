package dashboard

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// handleClick processes a mouse click: focus pane, select row, or activate
// a heading/button region.
func (m model) handleClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	x, y := mouse.X, mouse.Y
	if m.footer != "" {
		m.footer = ""
	}
	if m.ov.kind == overlayMenu {
		return m.menuClick(x, y)
	}
	if m.ov.kind == overlayConfirm {
		m.ov = overlayState{}
		return m, nil
	}

	// Header buttons. Geometry is recomputed from the same layout helper that
	// renders the header; render-time model copies cannot persist hit regions.
	_, regions := m.headerRows()
	for _, r := range regions {
		if r.w <= 0 || !inRegion(x, y, r) {
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
		case "integrations":
			m.ov = m.newIntegrationsOverlay()
			return m, moshiStatusCmd()
		case "help":
			m.ov = m.newHelpOverlay()
			return m, nil
		case "menu":
			m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: m.mainMenuItems()}
			return m, nil
		}
	}

	if mouse.Button == tea.MouseLeft {
		if split, ok := m.splitAt(x, y); ok {
			m.dragSplit = split
			m.applySplitDrag(split, x, y)
			return m, nil
		}
	}

	// Pane rows: find which pane contains (x, y) by replaying layout geometry.
	p, localRow, paneX, paneW, ok := m.panelAt(x, y)
	if !ok {
		return m, nil
	}
	m.focused = p
	contentW := paneW - 2
	if p == panelStats {
		contentW = paneW - 3 // borders plus the inset scrollbar column.
	}
	// Stats header click cycles sort on the rendered process-table column.
	if p == panelStats && localRow == 0 {
		columns, keys := statsColumnsForWidth(contentW)
		column := statsColumnAt(x-paneX-1, columns)
		if column >= 0 && column < len(keys) {
			m.cycleStatsSort(keys[column])
		}
		return m, nil
	}
	if localRow >= 1 || (p != panelStats && localRow >= 0) {
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
func (m model) handleMouseMotion(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if m.dragSplit == "" {
		return m, nil
	}
	m.applySplitDrag(m.dragSplit, mouse.X, mouse.Y)
	return m, nil
}

func (m model) handleMouseRelease(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if m.dragSplit == "" {
		return m, nil
	}
	m.applySplitDrag(m.dragSplit, mouse.X, mouse.Y)
	m.dragSplit = ""
	if err := m.saveDashboardConfig(); err != nil {
		m.footer = "save pane splits: " + err.Error()
		return m, nil
	}
	m.footer = "pane splits saved"
	m.footerSeq++
	return m, footerClearCmd(m.footerSeq)
}

// --- pane hit testing continues below ---

// handleWheel scrolls the pane under the cursor.
func (m model) handleWheel(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if m.footer != "" {
		m.footer = ""
	}
	if m.ov.kind == overlayMenu {
		direction := 1
		if mouse.Button == tea.MouseWheelUp {
			direction = -1
		}
		if m.ov.menuSel < len(m.ov.menuItems) {
			action := m.ov.menuItems[m.ov.menuSel].action
			if strings.HasPrefix(action, "inline:") {
				return m.cycleMenuValue(action, direction), nil
			}
			if action == "main.theme" || action == "main.view" {
				return m.cycleMainValue(action, direction), nil
			}
		}
		m.ov.menuSel = max(0, min(len(m.ov.menuItems)-1, m.ov.menuSel+direction))
		return m, nil
	}
	if m.ov.kind == overlayViewport {
		var cmd tea.Cmd
		m.ov.vp, cmd = m.ov.vp.Update(tea.MouseWheelMsg(mouse))
		return m, cmd
	}
	p, _, _, _, ok := m.panelAt(mouse.X, mouse.Y)
	if !ok {
		return m, nil
	}
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.scroll[p] -= 3
		if m.scroll[p] < 0 {
			m.scroll[p] = 0
		}
	case tea.MouseWheelDown:
		m.scroll[p] += 3
		maximum := m.maxScroll(p)
		if m.scroll[p] > maximum {
			m.scroll[p] = maximum
		}
	}
	return m, nil
}

// maxScroll computes the largest valid scroll offset for a pane.
func (m model) maxScroll(panel panelID) int {
	var total int
	switch panel {
	case panelStatus:
		total = len(m.sortedProviders())
	case panelFavourites:
		total = len(m.favouriteList())
	case panelUsage:
		total = m.usageLineCount()
	case panelStats:
		total = len(m.statsRows())
	}
	return max(0, total-m.selectableViewportHeight(panel))
}

// menuClick maps the btop-style two-row option geometry to one menu item.
func (m model) menuClick(x, y int) (tea.Model, tea.Cmd) {
	start, end := m.menuWindow()
	leftWidth, rightWidth := m.menuWidths()
	contentWidth := leftWidth + rightWidth + 1
	contentHeight := 2 + (end-start)*2 + 1
	boxWidth, boxHeight := contentWidth+4, contentHeight+2
	startX, startY := (m.width-boxWidth)/2, (m.height-boxHeight)/2
	itemY := y - (startY + 3)
	if x < startX || x >= startX+boxWidth || itemY < 0 || itemY >= (end-start)*2 {
		m.ov = overlayState{}
		return m, nil
	}
	row := start + itemY/2
	m.ov.menuSel = row
	action := m.ov.menuItems[row].action
	if strings.HasPrefix(action, "inline:") {
		direction := 1
		if x < startX+2+leftWidth/2 {
			direction = -1
		}
		return m.cycleMenuValue(action, direction), nil
	}
	if action == "main.theme" || action == "main.view" {
		direction := 1
		if x < startX+2+leftWidth/2 {
			direction = -1
		}
		return m.cycleMainValue(action, direction), nil
	}
	return m.runMenuAction(action)
}

// panelAt maps screen coordinates through the same geometry used by render.
func (m model) panelAt(x, y int) (panelID, int, int, int, bool) {
	bodyY := y - headerLines
	if bodyY < 0 || y >= m.height-1 {
		return 0, 0, 0, 0, false
	}
	for _, rect := range m.paneLayout() {
		if x < rect.x || x >= rect.x+rect.w || bodyY < rect.y || bodyY >= rect.y+rect.h {
			continue
		}
		localRow := bodyY - rect.y - 1
		return rect.panel, localRow, rect.x, rect.w, true
	}
	return 0, 0, 0, 0, false
}

func inRegion(x, y int, r hitRegion) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}
