package dashboard

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/chriisbusy/status-slug/internal/config"
)

// handleClick processes a mouse click: focus pane, select row, or activate
// a heading/button region.
func (m model) handleClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	x, y := mouse.X, mouse.Y
	if m.footer != "" {
		m.footer = ""
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
			m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: []menuItem{
				{"add provider", "main.add"},
				{"settings", "main.settings"},
				{"cycle theme", "main.theme"},
				{"cycle view", "main.view"},
				{"Moshi / integrations", "main.integrations"},
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
	if err := config.Save(m.cfg); err != nil {
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
func (m model) maxScroll(panel panelID) int {
	var total int
	switch panel {
	case panelStatus:
		total = len(m.sortedProviders())
	case panelFavourites:
		total = len(m.favouriteList())
	case panelUsage:
		content := m.renderUsagePane(80, 1<<20, false)
		total = strings.Count(content, "\n") + 1
	case panelStats:
		total = len(m.statsRows())
	}
	return max(0, total-m.selectableViewportHeight(panel))
}

// menuClick maps a click to a menu row when the menu overlay is open.
func (m model) menuClick(x, y int) (tea.Model, tea.Cmd) {
	items := len(m.ov.menuItems)
	overlayHeight := items + 4
	overlayWidth := 0
	for _, item := range m.ov.menuItems {
		overlayWidth = max(overlayWidth, len(item.label)+4)
	}
	overlayWidth = max(24, overlayWidth)
	startX := (m.width - overlayWidth) / 2
	startY := (m.height - overlayHeight) / 2
	row := y - startY - 1
	if row >= 0 && row < items && x >= startX && x < startX+overlayWidth {
		m.ov.menuSel = row
		return m.runMenuAction(m.ov.menuItems[row].action)
	}
	m.ov = overlayState{}
	return m, nil
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
