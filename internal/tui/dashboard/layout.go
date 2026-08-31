package dashboard

import (
	"sort"

	"github.com/chriisbusy/status-slug/internal/config"
)

type paneRect struct {
	panel      panelID
	x, y, w, h int
}

const (
	statusMinWidth  = 70
	statusMinHeight = 11
	statsMinWidth   = 48
	statsMinHeight  = 10
	usageMinWidth   = 40
	usageMinHeight  = 8
	favMinWidth     = 40
	favMinHeight    = 8
)

func (m model) bodySize() (int, int) {
	return m.width, max(1, m.height-headerLines-1)
}

func (m model) admittedPanels() []panelID {
	configured := m.visiblePanels()
	width, height := m.bodySize()
	admitted := make([]panelID, 0, len(configured))
	for _, panel := range configured {
		candidate := append(append([]panelID(nil), admitted...), panel)
		if len(admitted) == 0 || panelsFit(candidate, width, height) {
			admitted = candidate
		}
	}
	if len(admitted) == 1 {
		for _, panel := range configured {
			if panel == m.focused {
				admitted[0] = panel
				break
			}
		}
	}
	return admitted
}

func panelsFit(panels []panelID, width, height int) bool {
	if len(panels) <= 1 {
		return width >= 1 && height >= 3
	}
	hasStatus := containsPanel(panels, panelStatus)
	hasStats := containsPanel(panels, panelStats)
	hasUsage := containsPanel(panels, panelUsage)
	hasFavourites := containsPanel(panels, panelFavourites)

	leftWidth, leftHeight := 0, 0
	if hasUsage {
		leftWidth = max(leftWidth, usageMinWidth)
		leftHeight += usageMinHeight
	}
	if hasFavourites {
		leftWidth = max(leftWidth, favMinWidth)
		leftHeight += favMinHeight
	}
	lowerWidth, lowerHeight := leftWidth, leftHeight
	if hasStats {
		if leftWidth > 0 {
			lowerWidth += statsMinWidth
		} else {
			lowerWidth = statsMinWidth
		}
		lowerHeight = max(lowerHeight, statsMinHeight)
	}
	if !hasStats && leftWidth == 0 {
		lowerWidth, lowerHeight = statusMinWidth, statusMinHeight
	}
	if hasStatus {
		return width >= max(statusMinWidth, lowerWidth) && height >= statusMinHeight+lowerHeight
	}
	return width >= lowerWidth && height >= lowerHeight
}

func containsPanel(panels []panelID, target panelID) bool {
	for _, panel := range panels {
		if panel == target {
			return true
		}
	}
	return false
}

func (m model) paneLayout() []paneRect {
	panels := m.admittedPanels()
	width, height := m.bodySize()
	if len(panels) == 0 {
		return nil
	}
	if len(panels) == 1 || m.zoomed {
		panel := panels[0]
		if m.zoomed {
			panel = m.focused
		}
		return []paneRect{{panel: panel, w: width, h: height}}
	}
	view := config.NormalizeView(m.activeViewDef())
	remaining := append([]panelID(nil), panels...)
	var rects []paneRect
	bodyY, bodyH := 0, height
	if containsPanel(remaining, panelStatus) {
		remaining = removePanel(remaining, panelStatus)
		lowerMin := lowerMinimumHeight(remaining)
		topHeight := splitDimension(height, view.TopRatio, statusMinHeight, lowerMin)
		rects = append(rects, paneRect{panel: panelStatus, w: width, h: topHeight})
		bodyY, bodyH = topHeight, height-topHeight
	}

	hasStats := containsPanel(remaining, panelStats)
	var left []panelID
	for _, panel := range remaining {
		if panel != panelStats {
			left = append(left, panel)
		}
	}
	leftX, leftWidth := 0, width
	if hasStats && len(left) > 0 {
		leftWidth = splitDimension(width, view.LeftRatio, lowerMinimumWidth(left), statsMinWidth)
		rects = append(rects, paneRect{panel: panelStats, x: leftWidth, y: bodyY, w: width - leftWidth, h: bodyH})
	} else if hasStats {
		rects = append(rects, paneRect{panel: panelStats, y: bodyY, w: width, h: bodyH})
		leftWidth = 0
	}
	if len(left) == 1 {
		rects = append(rects, paneRect{panel: left[0], x: leftX, y: bodyY, w: leftWidth, h: bodyH})
	} else if len(left) > 1 {
		firstHeight := splitDimension(bodyH, view.UsageRatio, panelMinHeight(left[0]), panelMinHeight(left[1]))
		rects = append(rects,
			paneRect{panel: left[0], x: leftX, y: bodyY, w: leftWidth, h: firstHeight},
			paneRect{panel: left[1], x: leftX, y: bodyY + firstHeight, w: leftWidth, h: bodyH - firstHeight},
		)
	}
	sort.SliceStable(rects, func(i, j int) bool {
		if rects[i].y == rects[j].y {
			return rects[i].x < rects[j].x
		}
		return rects[i].y < rects[j].y
	})
	return rects
}

func removePanel(panels []panelID, target panelID) []panelID {
	out := make([]panelID, 0, len(panels)-1)
	for _, panel := range panels {
		if panel != target {
			out = append(out, panel)
		}
	}
	return out
}

func lowerMinimumHeight(panels []panelID) int {
	left := 0
	if containsPanel(panels, panelUsage) {
		left += usageMinHeight
	}
	if containsPanel(panels, panelFavourites) {
		left += favMinHeight
	}
	if containsPanel(panels, panelStats) {
		return max(left, statsMinHeight)
	}
	return max(1, left)
}

func lowerMinimumWidth(panels []panelID) int {
	width := 0
	for _, panel := range panels {
		width = max(width, panelMinWidth(panel))
	}
	return width
}

func panelMinWidth(panel panelID) int {
	switch panel {
	case panelStats:
		return statsMinWidth
	case panelUsage:
		return usageMinWidth
	case panelFavourites:
		return favMinWidth
	default:
		return statusMinWidth
	}
}

func panelMinHeight(panel panelID) int {
	switch panel {
	case panelStats:
		return statsMinHeight
	case panelUsage:
		return usageMinHeight
	case panelFavourites:
		return favMinHeight
	default:
		return statusMinHeight
	}
}

func splitDimension(total int, ratio float64, firstMin, secondMin int) int {
	if total <= firstMin+secondMin {
		return max(1, min(total-1, firstMin))
	}
	first := int(float64(total)*ratio + 0.5)
	return max(firstMin, min(total-secondMin, first))
}

func (m model) splitAt(x, y int) (string, bool) {
	bodyY := y - headerLines
	if bodyY < 0 {
		return "", false
	}
	rects := m.paneLayout()
	for _, rect := range rects {
		if rect.panel == panelStatus && len(rects) > 1 && bodyY == rect.y+rect.h-1 {
			return "top", true
		}
	}
	for _, rect := range rects {
		if rect.panel == panelStats && rect.x > 0 && bodyY >= rect.y && bodyY < rect.y+rect.h && nearCell(x, rect.x) {
			return "left", true
		}
	}
	var usage, favourites *paneRect
	for index := range rects {
		switch rects[index].panel {
		case panelUsage:
			usage = &rects[index]
		case panelFavourites:
			favourites = &rects[index]
		}
	}
	if usage != nil && favourites != nil && usage.x == favourites.x && usage.w == favourites.w {
		boundary := usage.y + usage.h
		if x >= usage.x && x < usage.x+usage.w && bodyY == boundary-1 {
			return "usage", true
		}
	}
	return "", false
}

func nearCell(value, boundary int) bool {
	return value == boundary || value == boundary-1
}

func (m *model) applySplitDrag(kind string, x, y int) {
	view := config.NormalizeView(m.activeViewDef())
	width, height := m.bodySize()
	bodyY := y - headerLines
	switch kind {
	case "top":
		view.TopRatio = clampRatio(float64(bodyY)/float64(max(1, height)), 0.20, 0.75)
	case "left":
		view.LeftRatio = clampRatio(float64(x)/float64(max(1, width)), 0.25, 0.75)
	case "usage":
		var usage, favourites *paneRect
		rects := m.paneLayout()
		for index := range rects {
			switch rects[index].panel {
			case panelUsage:
				usage = &rects[index]
			case panelFavourites:
				favourites = &rects[index]
			}
		}
		if usage != nil && favourites != nil {
			total := usage.h + favourites.h
			view.UsageRatio = clampRatio(float64(bodyY-usage.y)/float64(max(1, total)), 0.25, 0.75)
		}
	}
	m.upsertUserView(config.NormalizeView(view))
}

func clampRatio(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
