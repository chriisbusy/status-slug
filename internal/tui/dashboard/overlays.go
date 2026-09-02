package dashboard

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
	"github.com/chriisbusy/status-slug/internal/tui/widgets"
)

// overlayKind identifies which overlay is open.
type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayConfirm
	overlayViewport // help / inspect
	overlayMenu     // panel menus
	overlayInput    // textinput prompt
	overlayForm     // huh form (settings, meter add/edit)
)

// menuItem is one selectable action in a menu overlay.
type menuItem struct {
	label  string
	action string
}

// overlayState carries all overlay-scoped state.
type overlayState struct {
	kind      overlayKind
	title     string
	body      string
	action    string     // confirm: action id
	menuItems []menuItem // menu
	menuSel   int
	input     *widgets.TextField       // input
	inputFor  string                   // what the input edits
	form      *widgets.Form            // form (settings, meter add/edit)
	fields    map[string]widgets.Field // form field refs by key
	formFor   string                   // "settings" | "meter:<provider>:<editName>"
	vp        viewport.Model           // viewport
}

// --- overlay key routing ---

func (m model) overlayKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch m.ov.kind {
	case overlayConfirm:
		switch key {
		case "y", "enter":
			return m.runConfirm(m.ov.action)
		default:
			m.ov = overlayState{}
			return m, nil
		}
	case overlayViewport:
		switch key {
		case "esc", "q", "?", "i":
			m.ov = overlayState{}
			return m, nil
		case "r":
			if m.ov.title == "integrations" {
				return m, moshiStatusCmd()
			}
		case "f":
			if m.ov.title == "integrations" {
				targets := m.staleMoshiTargets()
				if len(targets) == 0 {
					m.footer = "no stale Moshi hooks"
					m.footerSeq++
					return m, footerClearCmd(m.footerSeq)
				}
				m.ov = overlayState{kind: overlayConfirm, action: "moshi.repair:" + strings.Join(targets, ","),
					title: "Repair stale Moshi hooks?", body: "Runs moshi-hook install for: " + strings.Join(targets, ", ")}
				return m, nil
			}
		case "enter":
			// From the inspect overlay: re-probe the inspected target.
			if m.ov.title == "inspect" && !m.checking {
				m.ov = overlayState{}
				if m.focused == panelStatus {
					if p := m.selectedProvider(); p != nil {
						cmd := m.startCheckOne(*p)
						return m, cmd
					}
				}
				if m.focused == panelFavourites {
					if pv, mod := m.selectedFavourite(); mod != nil {
						cmd := m.startCheckModel(*pv, *mod)
						return m, cmd
					}
				}
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.ov.vp, cmd = m.ov.vp.Update(msg)
		return m, cmd
	case overlayMenu:
		return m.menuKey(key)
	case overlayInput:
		return m.inputKey(msg)
	case overlayForm:
		return m.formKey(msg)
	}
	m.ov = overlayState{}
	return m, nil
}

// forwardToOverlay routes non-key messages (blink ticks, mouse) to the
// active overlay component.
func (m model) forwardToOverlay(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.ov.kind {
	case overlayForm:
		switch msg := msg.(type) {
		case dashBlinkMsg:
			m.ov.form.Tick()
			return m, dashBlinkTick()
		case tea.MouseClickMsg:
			x, y := m.overlayLocal(msg.X, msg.Y)
			m.ov.form.HandleClick(x, y, m.overlayFormWidth())
			if m.ov.form.Done() {
				return m.completeForm()
			}
			return m, nil
		case tea.MouseWheelMsg:
			key := "down"
			if msg.Button == tea.MouseWheelUp {
				key = "up"
			}
			m.ov.form.HandleKey(key)
			return m, nil
		}
		return m, nil
	case overlayViewport:
		var cmd tea.Cmd
		m.ov.vp, cmd = m.ov.vp.Update(msg)
		return m, cmd
	case overlayInput:
		if _, ok := msg.(dashBlinkMsg); ok {
			m.ov.input.Tick()
			return m, dashBlinkTick()
		}
		return m, nil
	}
	return m, nil
}

// dashBlinkMsg drives cursor blink inside dashboard overlays.
type dashBlinkMsg struct{}

func dashBlinkTick() tea.Cmd {
	return tea.Tick(600*time.Millisecond, func(time.Time) tea.Msg { return dashBlinkMsg{} })
}

// overlayFormWidth is the content width of overlay forms.
func (m model) overlayFormWidth() int {
	w := 64
	if m.width > 0 && m.width-10 < w {
		w = m.width - 10
	}
	if w < 36 {
		w = 36
	}
	return w
}

// overlayLocal converts screen coords to overlay-local coords for the
// centered form popup.
func (m model) overlayLocal(x, y int) (int, int) {
	boxW := m.overlayFormWidth() + 4
	startX := (m.width - boxW) / 2
	if startX < 0 {
		startX = 0
	}
	boxH := m.overlayHeight() + 2
	startY := (m.height - boxH) / 2
	if startY < 0 {
		startY = 0
	}
	return x - startX - 2, y - startY - 1
}

// --- confirm ---

func (m model) runConfirm(action string) (tea.Model, tea.Cmd) {
	m.ov = overlayState{}
	switch {
	case action == "quit":
		return m, tea.Quit
	case strings.HasPrefix(action, "moshi.repair:"):
		targets := strings.Split(strings.TrimPrefix(action, "moshi.repair:"), ",")
		return m, moshiRepairCmd(targets)
	case strings.HasPrefix(action, "remove:"):
		name := strings.TrimPrefix(action, "remove:")
		if p := m.cfg.Find(name); p != nil {
			_ = secret.Delete(p.KeyRef)
			m.cfg.Remove(name)
			if err := config.Save(m.cfg); err != nil {
				m.footer = "save config: " + err.Error()
			} else {
				m.footer = fmt.Sprintf("removed %s", name)
			}
		}
		return m, nil
	case action == "reset-stats":
		for _, ps := range m.st.Providers {
			ps.Counters = state.Counters{}
			for _, ms := range ps.Models {
				ms.Counters = state.Counters{}
			}
		}
		_ = m.st.Save()
		m.footer = "counters reset"
		return m, nil
	case strings.HasPrefix(action, "remove-meter:"):
		// remove-meter:<provider>/<meter>
		ref := strings.TrimPrefix(action, "remove-meter:")
		provName, meterName, _ := strings.Cut(ref, "/")
		if p := m.cfg.Find(provName); p != nil {
			for i, mt := range p.Meters {
				if mt.Name == meterName {
					p.Meters = append(p.Meters[:i], p.Meters[i+1:]...)
					break
				}
			}
			delete(m.st.Meters, provName+"/"+meterName)
			if err := config.Save(m.cfg); err != nil {
				m.footer = "save config: " + err.Error()
			} else {
				_ = m.st.Save()
				m.footer = fmt.Sprintf("removed meter %s", meterName)
			}
		}
		return m, nil
	case strings.HasPrefix(action, "remove-fav:"):
		ref := strings.TrimPrefix(action, "remove-fav:")
		provName, modelID, _ := strings.Cut(ref, "/")
		if p := m.cfg.Find(provName); p != nil {
			for i := range p.Models {
				if p.Models[i].ID == modelID {
					p.Models[i].Favourite = false
				}
			}
			if err := config.Save(m.cfg); err != nil {
				m.footer = "save config: " + err.Error()
			} else {
				m.footer = fmt.Sprintf("unfavourited %s", modelID)
			}
		}
		return m, nil
	}
	return m, nil
}

// --- menus ---

func (m model) mainMenuItems() []menuItem {
	return []menuItem{
		{"panel order  ‹ " + strings.Join(m.activeViewDef().Panels, " · ") + " ›", "inline:panel-order"},
		{"view  ‹ " + m.activeViewDef().Name + " ›", "main.view"},
		{"theme  ‹ " + m.cfg.Settings.Theme + " ›", "main.theme"},
		{"status panel", "main.panel:status"},
		{"usage panel", "main.panel:usage"},
		{"favourites panel", "main.panel:favourites"},
		{"stats panel", "main.panel:stats"},
		{"add provider or integration", "main.add"},
		{"integrations", "main.integrations"},
		{"settings", "main.settings"},
		{"help", "main.help"},
		{"quit", "main.quit"},
	}
}

func (m model) newMenuOverlay(p panelID) overlayState {
	ov := overlayState{kind: overlayMenu, title: panelNames[p] + " menu"}
	switch p {
	case panelStatus:
		ov.menuItems = []menuItem{
			{"sort  ‹ " + m.prefs.statusSort + " ›", "inline:status-sort"},
			{"group by label  ‹ " + onOff(m.prefs.statusGroup) + " ›", "inline:status-group"},
			{"check selected", "status.check"},
			{"edit selected provider", "status.edit"},
			{fmt.Sprintf("%s selected provider", enableDisableLabel(m.selectedProvider())), "status.toggle-enabled"},
			{"remove selected provider", "status.remove"},
		}
	case panelUsage:
		ov.menuItems = []menuItem{
			{"refresh meters", "usage.refresh"},
			{"set meter value", "usage.set"},
			{"add meter", "usage.add"},
			{"edit meter", "usage.edit"},
			{"remove meter", "usage.remove"},
		}
	case panelFavourites:
		ov.menuItems = []menuItem{
			{"sort  ‹ " + m.prefs.favSort + " ›", "inline:fav-sort"},
			{"add from known models", "fav.add-known"},
			{"add custom model", "fav.add-custom"},
			{"toggle selected probe mode", "fav.toggle-probe"},
			{"re-probe selected", "fav.reprobe"},
			{"remove selected favourite", "fav.remove"},
		}
	case panelStats:
		sortValue := m.prefs.statsSort
		if sortValue == "" {
			sortValue = "natural"
		}
		ov.menuItems = []menuItem{
			{"sort  ‹ " + sortValue + " ›", "inline:stats-sort"},
			{"favourite rows  ‹ " + onOff(m.prefs.statsShowFavs) + " ›", "inline:stats-favs"},
			{"reset counters", "stats.reset"},
		}
	}
	return ov
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (m model) cycleMenuValue(action string, direction int) model {
	selected := m.ov.menuSel
	cycle := func(values []string, current string) string {
		index := 0
		for i, value := range values {
			if value == current {
				index = i
				break
			}
		}
		return values[(index+direction+len(values))%len(values)]
	}
	switch action {
	case "inline:status-sort":
		m.prefs.statusSort = cycle([]string{"name", "status", "latency", "checked"}, m.prefs.statusSort)
		m.ov = m.newMenuOverlay(panelStatus)
	case "inline:status-group":
		m.prefs.statusGroup = !m.prefs.statusGroup
		m.ov = m.newMenuOverlay(panelStatus)
	case "inline:panel-order":
		view := m.activeViewDef()
		if len(view.Panels) > 1 {
			panels := append([]string(nil), view.Panels...)
			if direction > 0 {
				panels = append(panels[1:], panels[0])
			} else {
				panels = append([]string{panels[len(panels)-1]}, panels[:len(panels)-1]...)
			}
			view.Panels = panels
			m.upsertUserView(view)
		}
		m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: m.mainMenuItems()}
	case "inline:fav-sort":
		m.prefs.favSort = cycle([]string{"name", "latency", "status"}, m.prefs.favSort)
		m.ov = m.newMenuOverlay(panelFavourites)
	case "inline:stats-sort":
		current := m.prefs.statsSort
		if current == "" {
			current = "natural"
		}
		next := cycle([]string{"natural", "name", "provider", "kind", "status", "latency", "p95", "age"}, current)
		if next == "natural" {
			next = ""
		}
		m.prefs.statsSort = next
		m.ov = m.newMenuOverlay(panelStats)
	case "inline:stats-favs":
		m.prefs.statsShowFavs = !m.prefs.statsShowFavs
		m.ov = m.newMenuOverlay(panelStats)
	}
	m.ov.menuSel = min(selected, max(0, len(m.ov.menuItems)-1))
	return m
}

// optionalFloat validates a huh input that may be blank or a float.
func optionalFloat(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return fmt.Errorf("must be a number")
	}
	return nil
}

func (m model) cycleMainValue(action string, direction int) model {
	selected := m.ov.menuSel
	cycle := func(values []string, current string) string {
		index := 0
		for i, value := range values {
			if value == current {
				index = i
				break
			}
		}
		return values[(index+direction+len(values))%len(values)]
	}
	switch action {
	case "main.theme":
		names := theme.BuiltinNames()
		if entries, err := os.ReadDir(config.ThemesDir()); err == nil {
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".theme") {
					names = append(names, strings.TrimSuffix(entry.Name(), ".theme"))
				}
			}
		}
		m.cfg.Settings.Theme = cycle(names, m.cfg.Settings.Theme)
		m.palette, m.themeWarns = theme.LoadFromSettings(m.cfg.Settings)
	case "main.view":
		m.st.UI.View = cycle(m.viewCycleOrder(), m.activeViewDef().Name)
	}
	m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: m.mainMenuItems(), menuSel: selected}
	return m
}

func (m model) menuKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "m":
		m.ov = overlayState{kind: overlayMenu, title: "menu", menuItems: m.mainMenuItems()}
		return m, nil
	case "s":
		m.ov = m.newMenuOverlay(panelStatus)
		return m, nil
	case "u":
		m.ov = m.newMenuOverlay(panelUsage)
		return m, nil
	case "f":
		m.ov = m.newMenuOverlay(panelFavourites)
		return m, nil
	case "t":
		m.ov = m.newMenuOverlay(panelStats)
		return m, nil
	case "g":
		m.ov = m.newIntegrationsOverlay()
		return m, moshiStatusCmd()
	case "o":
		m.ov = m.newSettingsOverlay()
		if m.ov.kind == overlayForm {
			return m, dashBlinkTick()
		}
		return m, nil
	case "esc", "q":
		_ = m.saveDashboardConfig()
		m.savePrefs()
		m.ov = overlayState{}
		return m, nil
	case "j", "down":
		if m.ov.menuSel < len(m.ov.menuItems)-1 {
			m.ov.menuSel++
		}
		return m, nil
	case "k", "up":
		if m.ov.menuSel > 0 {
			m.ov.menuSel--
		}
		return m, nil
	case "left", "h", "right", "l":
		if m.ov.menuSel < len(m.ov.menuItems) {
			action := m.ov.menuItems[m.ov.menuSel].action
			if strings.HasPrefix(action, "inline:") {
				direction := 1
				if key == "left" || key == "h" {
					direction = -1
				}
				return m.cycleMenuValue(action, direction), nil
			}
			if action == "main.theme" || action == "main.view" {
				direction := 1
				if key == "left" || key == "h" {
					direction = -1
				}
				return m.cycleMainValue(action, direction), nil
			}
		}
	case "enter", " ", "space":
		if m.ov.menuSel >= len(m.ov.menuItems) {
			m.ov = overlayState{}
			return m, nil
		}
		action := m.ov.menuItems[m.ov.menuSel].action
		if strings.HasPrefix(action, "inline:") {
			return m.cycleMenuValue(action, 1), nil
		}
		if action == "main.theme" || action == "main.view" {
			return m.cycleMainValue(action, 1), nil
		}
		return m.runMenuAction(action)
	}
	return m, nil
}

// runMenuAction executes a menu action id.
func (m model) runMenuAction(action string) (tea.Model, tea.Cmd) {
	previousMenu := m.ov
	_ = m.saveDashboardConfig()
	m.savePrefs()
	m.ov = overlayState{}
	switch {
	case strings.HasPrefix(action, "main."):
		switch strings.TrimPrefix(action, "main.") {
		case "add":
			m.openWizard("")
			if m.wiz != nil {
				return m, m.wiz.Init()
			}
		case "settings":
			m.ov = m.newSettingsOverlay()
			if m.ov.kind == overlayForm {
				return m, dashBlinkTick()
			}
		case "panel:status":
			m.ov = m.newMenuOverlay(panelStatus)
		case "panel:usage":
			m.ov = m.newMenuOverlay(panelUsage)
		case "panel:favourites":
			m.ov = m.newMenuOverlay(panelFavourites)
		case "panel:stats":
			m.ov = m.newMenuOverlay(panelStats)
		case "theme":
			next := m.cycleTheme()
			next.ov = previousMenu
			return next, footerClearCmd(next.footerSeq)
		case "view":
			next := m.cycleView()
			next.ov = previousMenu
			return next, footerClearCmd(next.footerSeq)
		case "integrations":
			m.ov = m.newIntegrationsOverlay()
			return m, moshiStatusCmd()
		case "help":
			m.ov = m.newHelpOverlay()
		case "quit":
			return m, tea.Quit
		}

	case strings.HasPrefix(action, "status.sort:"):
		m.prefs.statusSort = strings.TrimPrefix(action, "status.sort:")
		m.savePrefs()
		m.ov = m.newMenuOverlay(panelStatus)
	case action == "status.group":
		m.prefs.statusGroup = !m.prefs.statusGroup
		m.savePrefs()
		m.ov = m.newMenuOverlay(panelStatus)
	case action == "status.check":
		if p := m.selectedProvider(); p != nil {
			cmd := m.startCheckOne(*p)
			return m, cmd
		}
	case action == "status.edit":
		if p := m.selectedProvider(); p != nil {
			m.openWizard(p.Name)
			if m.wiz != nil {
				return m, m.wiz.Init()
			}
		}
	case action == "status.toggle-enabled":
		if p := m.selectedProvider(); p != nil {
			p.Enabled = !p.Enabled
			if err := config.Save(m.cfg); err != nil {
				m.footer = "save config: " + err.Error()
			} else if p.Enabled {
				m.footer = p.Name + " enabled"
			} else {
				m.footer = p.Name + " disabled"
			}
		}
	case action == "status.remove":
		if p := m.selectedProvider(); p != nil {
			m.ov = overlayState{kind: overlayConfirm, action: "remove:" + p.Name,
				title: fmt.Sprintf("Remove provider %q?", p.Name),
				body:  "Its stored key will be deleted too."}
		}

	case action == "usage.set":
		cmd := m.openSetValueInput()
		return m, cmd
	case action == "usage.refresh":
		if !m.checking {
			cmd := m.startCheckAll() // auto meters refresh on check
			return m, cmd
		}
	case action == "usage.add":
		cmd := m.openMeterForm("")
		return m, cmd
	case action == "usage.edit":
		// Edit the meter under the usage selection.
		if entry := m.selectedMeterEntry(); entry != nil && entry.meter != nil {
			cmd := m.openMeterForm(entry.meter.Name)
			return m, cmd
		} else {
			m.footer = "select a meter row first"
		}
	case action == "usage.remove":
		if entry := m.selectedMeterEntry(); entry != nil && entry.meter != nil {
			m.ov = overlayState{kind: overlayConfirm,
				action: "remove-meter:" + entry.provider + "/" + entry.meter.Name,
				title:  fmt.Sprintf("Remove meter %q from %s?", entry.meter.Name, entry.provider)}
		} else {
			m.footer = "select a meter row first"
		}

	case action == "fav.add-known":
		return m, m.openFavKnownMenu()
	case action == "fav.add-custom":
		m.ov = m.newInputOverlay("custom model id", "fav-custom", "e.g. gpt-5-mini")
		return m, dashBlinkTick()
	case action == "fav.toggle-probe":
		if pv, mod := m.selectedFavourite(); mod != nil {
			if mod.Probe == "chat" {
				mod.Probe = "models"
			} else {
				mod.Probe = "chat"
			}
			_ = pv
			if err := config.Save(m.cfg); err != nil {
				m.footer = "save config: " + err.Error()
			} else {
				m.footer = fmt.Sprintf("%s probe=%s", mod.ID, mod.Probe)
			}
		}
	case action == "fav.remove":
		if pv, mod := m.selectedFavourite(); mod != nil {
			m.ov = overlayState{kind: overlayConfirm,
				action: "remove-fav:" + pv.Name + "/" + mod.ID,
				title:  fmt.Sprintf("Remove favourite %q?", mod.ID)}
		}
	case action == "fav.reprobe":
		if pv, mod := m.selectedFavourite(); mod != nil && !m.checking {
			cmd := m.startCheckModel(*pv, *mod)
			return m, cmd
		}
	case strings.HasPrefix(action, "fav.sort:"):
		m.prefs.favSort = strings.TrimPrefix(action, "fav.sort:")
		m.savePrefs()

	case action == "stats.sort":
		m.ov = overlayState{kind: overlayMenu, title: "stats sort", menuItems: []menuItem{
			{"program", "stats.sortcol:name"},
			{"provider", "stats.sortcol:provider"},
			{"kind", "stats.sortcol:kind"},
			{"status", "stats.sortcol:status"},
			{"latency", "stats.sortcol:latency"},
			{"p95", "stats.sortcol:p95"},
		}}
		return m, nil
	case strings.HasPrefix(action, "stats.sortcol:"):
		col := strings.TrimPrefix(action, "stats.sortcol:")
		if m.prefs.statsSort == col {
			m.prefs.statsSortDir = (m.prefs.statsSortDir + 1) % 3
		} else {
			m.prefs.statsSort = col
			m.prefs.statsSortDir = 1
		}
		m.savePrefs()
	case action == "stats.favs":
		m.prefs.statsShowFavs = !m.prefs.statsShowFavs
		m.savePrefs()
	case action == "stats.reset":
		m.ov = overlayState{kind: overlayConfirm, action: "reset-stats",
			title: "Reset all probe counters?", body: "Latency rings are kept."}

	case strings.HasPrefix(action, "fav-known:"):
		// fav-known:<provider>/<model id>
		ref := strings.TrimPrefix(action, "fav-known:")
		provName, modelID, _ := strings.Cut(ref, "/")
		if p := m.cfg.Find(provName); p != nil {
			for i := range p.Models {
				if p.Models[i].ID == modelID {
					p.Models[i].Favourite = true
				}
			}
			if err := config.Save(m.cfg); err != nil {
				m.footer = "save config: " + err.Error()
			} else {
				m.footer = fmt.Sprintf("favourited %s", modelID)
			}
		}
	}
	return m, nil
}

// selectedMeterEntry returns the usage entry under the selection, if any.
func (m model) selectedMeterEntry() *usageEntry {
	entries := m.usageEntries()
	if m.sel[panelUsage] < len(entries) {
		return &entries[m.sel[panelUsage]]
	}
	return nil
}

// openFavKnownMenu opens a menu of non-favourite known models.
func (m model) openFavKnownMenu() tea.Cmd {
	var items []menuItem
	for i := range m.cfg.Providers {
		p := &m.cfg.Providers[i]
		for _, mod := range p.Models {
			if !mod.Favourite {
				items = append(items, menuItem{
					label:  p.Name + " / " + mod.ID,
					action: "fav-known:" + p.Name + "/" + mod.ID,
				})
			}
		}
	}
	if len(items) == 0 {
		m.footer = "no unfavourited models — add a custom id instead"
		return nil
	}
	m.ov = overlayState{kind: overlayMenu, title: "add favourite", menuItems: items}
	return nil
}

// --- text input ---

func (m model) newInputOverlay(title, inputFor, placeholder string) overlayState {
	ti := widgets.NewText(m.palette, title, placeholder)
	ti.Focus()
	return overlayState{kind: overlayInput, title: title, inputFor: inputFor, input: ti}
}

// openSetValueInput opens a textinput for the selected meter.
func (m *model) openSetValueInput() tea.Cmd {
	entry := m.selectedMeterEntry()
	if entry == nil || entry.meter == nil {
		m.footer = "select a meter row first (usage pane)"
		return nil
	}
	m.ov = m.newInputOverlay(
		fmt.Sprintf("set %s/%s (%s)", entry.provider, entry.meter.Name, entry.meter.Unit),
		"meter:"+entry.provider+"/"+entry.meter.Name,
		"current value")
	return dashBlinkTick()
}

func (m model) inputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.ov = overlayState{}
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.ov.input.Value)
		for_ := m.ov.inputFor
		m.ov = overlayState{}
		return m.handleInputSubmit(for_, val)
	}
	m.ov.input.HandleKey(msg.String())
	return m, nil
}

func (m model) handleInputSubmit(inputFor, val string) (tea.Model, tea.Cmd) {
	switch {
	case strings.HasPrefix(inputFor, "meter:"):
		ref := strings.TrimPrefix(inputFor, "meter:")
		provName, meterName, _ := strings.Cut(ref, "/")
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			m.footer = "not a number: " + val
			return m, nil
		}
		m.st.SetMeter(provName, meterName, f)
		if err := m.st.Save(); err != nil {
			m.footer = "save state: " + err.Error()
		} else {
			m.footer = fmt.Sprintf("%s/%s = %.4g", provName, meterName, f)
		}
		return m, nil
	case inputFor == "fav-custom":
		if val == "" {
			return m, nil
		}
		// Attach to the provider selected in the status pane, else first.
		p := m.selectedProvider()
		if p == nil && len(m.cfg.Providers) > 0 {
			p = &m.cfg.Providers[0]
		}
		if p == nil {
			m.footer = "no providers"
			return m, nil
		}
		for _, mod := range p.Models {
			if mod.ID == val {
				m.footer = val + " already exists"
				return m, nil
			}
		}
		p.Models = append(p.Models, config.Model{ID: val, Favourite: true, Probe: "chat"})
		if err := config.Save(m.cfg); err != nil {
			m.footer = "save config: " + err.Error()
		} else {
			m.footer = "added favourite " + val
		}
		return m, nil
	}
	return m, nil
}

// --- meter form (widgets) ---

func (m *model) openMeterForm(editName string) tea.Cmd {
	// Which provider? Selected in status pane, else first enabled.
	p := m.selectedProvider()
	if p == nil && len(m.cfg.Providers) > 0 {
		p = &m.cfg.Providers[0]
	}
	if p == nil {
		m.footer = "no providers"
		return nil
	}

	var name, unit, usedStr, capStr, reset, resetDay string
	unit = "USD"
	reset = "never"
	if editName != "" {
		for _, mt := range p.Meters {
			if mt.Name == editName {
				name = mt.Name
				unit = mt.Unit
				if mt.Used != 0 {
					usedStr = strconv.FormatFloat(mt.Used, 'g', -1, 64)
				}
				if mt.Cap != 0 {
					capStr = strconv.FormatFloat(mt.Cap, 'g', -1, 64)
				}
				reset = mt.Reset
			}
		}
	}
	resetKind := "never"
	if k, _, ok := strings.Cut(reset, ":"); ok {
		resetKind = k
		if k == "monthly" || k == "weekly" || k == "date" {
			_, resetDay, _ = strings.Cut(reset, ":")
		}
	}

	nameF := widgets.NewText(m.palette, "meter name", "Energy, Spend, Requests…")
	nameF.Value = name
	nameF.Validate = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("name required")
		}
		return nil
	}
	unitF := widgets.NewText(m.palette, "unit", "USD")
	unitF.Value = unit
	usedF := widgets.NewText(m.palette, "current value (blank = 0)", "0")
	usedF.Value = usedStr
	usedF.Validate = widgets.OptionalFloat
	capF := widgets.NewText(m.palette, "cap (blank = no cap)", "1000")
	capF.Value = capStr
	capF.Validate = widgets.OptionalFloat
	resetF := widgets.NewSelect(m.palette, "reset", []string{
		"never", "monthly:<day>", "weekly:<weekday>", "date:<YYYY-MM-DD>",
	})
	resetKeys := []string{"never", "monthly", "weekly", "date"}
	for i, k := range resetKeys {
		if k == resetKind {
			resetF.Selected = i
		}
	}
	argF := widgets.NewText(m.palette, "reset argument", "day 1-31 / mon..sun / YYYY-MM-DD")
	argF.Value = resetDay

	m.ov = overlayState{
		kind:    overlayForm,
		title:   "meter: " + p.Name,
		form:    widgets.NewForm(m.palette, "", "", nameF, unitF, usedF, capF, resetF, argF),
		fields:  map[string]widgets.Field{"name": nameF, "unit": unitF, "used": usedF, "cap": capF, "reset": resetF, "resetArg": argF},
		formFor: "meter:" + p.Name + ":" + editName,
	}
	return dashBlinkTick()
}

// --- settings form (huh) ---

// --- settings form (widgets) ---

func (m model) newSettingsOverlay() overlayState {
	s := m.cfg.Settings

	themeNames := theme.BuiltinNames()
	if entries, err := os.ReadDir(config.ThemesDir()); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".theme") {
				themeNames = append(themeNames, strings.TrimSuffix(e.Name(), ".theme"))
			}
		}
	}
	sort.Strings(themeNames)

	selectIdx := func(f *widgets.SelectField, options []string, cur string) {
		for i, o := range options {
			if o == cur {
				f.Selected = i
			}
		}
	}

	themeF := widgets.NewSelect(m.palette, "theme", themeNames)
	if s.Theme == "" {
		s.Theme = "sstop"
	}
	selectIdx(themeF, themeNames, s.Theme)

	viewNames := m.viewCycleOrder()
	viewF := widgets.NewSelect(m.palette, "view", viewNames)
	activeView := config.NormalizeView(m.activeViewDef())
	selectIdx(viewF, viewNames, activeView.Name)
	ratioField := func(label string, current float64, low int) *widgets.SelectField {
		var options []string
		for percent := low; percent <= 75; percent += 5 {
			options = append(options, fmt.Sprintf("%d%%", percent))
		}
		field := widgets.NewSelect(m.palette, label, options)
		selectIdx(field, options, fmt.Sprintf("%d%%", int(current*100+0.5)))
		return field
	}
	topRatioF := ratioField("top split", activeView.TopRatio, 20)
	leftRatioF := ratioField("left split", activeView.LeftRatio, 25)
	usageRatioF := ratioField("usage split", activeView.UsageRatio, 25)

	borderF := widgets.NewSelect(m.palette, "border style", []string{"rounded", "square", "thick"})
	selectIdx(borderF, []string{"rounded", "square", "thick"}, s.BorderStyle)

	glyphF := widgets.NewSelect(m.palette, "graph style", []string{"tty", "block", "braille"})
	selectIdx(glyphF, []string{"tty", "block", "braille"}, s.GraphStyle)
	statsModeF := widgets.NewSelect(m.palette, "stats display", []string{"auto", "table", "graph"})
	selectIdx(statsModeF, []string{"auto", "table", "graph"}, s.StatsMode)

	bgF := widgets.NewConfirm(m.palette, "paint theme background", s.ThemeBackground)

	timeoutF := widgets.NewText(m.palette, "probe timeout (5-30s)", "10")
	timeoutF.Value = strconv.Itoa(s.ProbeTimeout)
	timeoutF.Validate = intRange(5, 30)

	refreshF := widgets.NewSelect(m.palette, "auto refresh", []string{"off", "30s", "60s", "300s"})
	refreshKeys := []string{"0", "30", "60", "300"}
	for i, k := range refreshKeys {
		if k == strconv.Itoa(s.AutoRefresh) {
			refreshF.Selected = i
		}
	}

	modeF := widgets.NewSelect(m.palette, "probe mode", []string{"models", "chat"})
	selectIdx(modeF, []string{"models", "chat"}, s.ProbeMode)

	histF := widgets.NewText(m.palette, "history length (20-240)", "60")
	histF.Value = strconv.Itoa(s.HistoryLength)
	histF.Validate = intRange(20, 240)

	keysF := widgets.NewSelect(m.palette, "keys source", []string{"auto", "keyring", "file", "env"})
	selectIdx(keysF, []string{"auto", "keyring", "file", "env"}, s.KeysSource)
	serveF := widgets.NewText(m.palette, "serve listen", "127.0.0.1:19777")
	serveF.Value = s.ServeListen
	serveF.Validate = validateLoopbackListen

	nerdF := widgets.NewConfirm(m.palette, "nerd font glyphs", s.NerdFont)
	quitF := widgets.NewConfirm(m.palette, "confirm quit", s.ConfirmQuit)
	launchF := widgets.NewConfirm(m.palette, "check on launch", s.CheckOnLaunch)
	bellF := widgets.NewConfirm(m.palette, "alert bell on down", s.AlertBell)

	panelToggles := map[string]bool{}
	for _, n := range panelNames {
		panelToggles[n] = false
	}
	for _, n := range m.activeViewDef().Panels {
		panelToggles[n] = true
	}
	pStatusF := widgets.NewConfirm(m.palette, "panel: status", panelToggles["status"])
	pUsageF := widgets.NewConfirm(m.palette, "panel: usage", panelToggles["usage"])
	pFavF := widgets.NewConfirm(m.palette, "panel: favourites", panelToggles["favourites"])
	pStatsF := widgets.NewConfirm(m.palette, "panel: stats", panelToggles["stats"])

	fields := []widgets.Field{
		widgets.NewNote(m.palette, "appearance", "theme, continuous pane splits, chrome"),
		themeF, viewF, topRatioF, leftRatioF, usageRatioF, borderF, glyphF, statsModeF, bgF,
		widgets.NewNote(m.palette, "probing", "timeouts, refresh, key storage"),
		timeoutF, refreshF, modeF, histF, keysF,
		widgets.NewNote(m.palette, "integrations", "press g for Moshi setup/status; loopback API setting below"),
		serveF,
		widgets.NewNote(m.palette, "behavior & panels", "toggles and pane visibility"),
		nerdF, quitF, launchF, bellF, pStatusF, pUsageF, pFavF, pStatsF,
	}

	return overlayState{
		kind:  overlayForm,
		title: "settings",
		form:  widgets.NewForm(m.palette, "", "", fields...),
		fields: map[string]widgets.Field{
			"theme": themeF, "view": viewF, "topRatio": topRatioF,
			"leftRatio": leftRatioF, "usageRatio": usageRatioF,
			"border": borderF, "glyphs": glyphF, "statsMode": statsModeF, "themeBackground": bgF,
			"timeout": timeoutF, "refresh": refreshF, "mode": modeF, "history": histF,
			"keys": keysF, "serveListen": serveF, "nerd": nerdF, "confirmQuit": quitF,
			"checkOnLaunch": launchF, "alertBell": bellF,
			"panel:status": pStatusF, "panel:usage": pUsageF,
			"panel:favourites": pFavF, "panel:stats": pStatsF,
		},
		formFor: "settings",
	}
}

func intRange(lo, hi int) func(string) error {
	return func(s string) error {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("must be %d-%d", lo, hi)
		}
		return nil
	}
}

func validateLoopbackListen(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("must be loopback")
	}
	return nil
}

func (m model) formKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		m.ov = overlayState{}
		return m, nil
	}
	if m.ov.form.HandleKey(msg.String()) {
		return m.completeForm()
	}
	return m, nil
}

// fieldVal reads a widget field's current value by kind.
func fieldVal(f widgets.Field) string {
	switch f := f.(type) {
	case *widgets.TextField:
		return f.Value
	case *widgets.SelectField:
		return f.Value()
	case *widgets.ConfirmField:
		if f.Value {
			return "true"
		}
		return "false"
	}
	return ""
}

func fieldBool(f widgets.Field) bool {
	if cf, ok := f.(*widgets.ConfirmField); ok {
		return cf.Value
	}
	return fieldVal(f) == "true"
}

// completeForm applies a completed widgets form.
func (m model) completeForm() (tea.Model, tea.Cmd) {
	get := func(k string) string { return fieldVal(m.ov.fields[k]) }
	getb := func(k string) bool { return fieldBool(m.ov.fields[k]) }

	switch {
	case m.ov.formFor == "settings":
		s := &m.cfg.Settings
		s.Theme = get("theme")
		s.BorderStyle = get("border")
		s.GraphStyle = get("glyphs")
		s.StatsMode = get("statsMode")
		s.ProbeTimeout, _ = strconv.Atoi(get("timeout"))
		s.AutoRefresh, _ = strconv.Atoi(map[string]string{"off": "0", "30s": "30", "60s": "60", "300s": "300"}[get("refresh")])
		s.ProbeMode = get("mode")
		s.HistoryLength, _ = strconv.Atoi(get("history"))
		s.KeysSource = get("keys")
		s.NerdFont = getb("nerd")
		s.ConfirmQuit = getb("confirmQuit")
		s.CheckOnLaunch = getb("checkOnLaunch")
		s.AlertBell = getb("alertBell")
		s.ThemeBackground = getb("themeBackground")
		s.ServeListen = strings.TrimSpace(get("serveListen"))

		// Apply view changes: materialize a builtin when it is customized.
		viewName := get("view")
		v := m.activeViewDef()
		if v.Name != viewName {
			m.st.UI.View = viewName
		} else {
			var panels []string
			for _, name := range panelNames {
				if getb("panel:" + name) {
					panels = append(panels, name)
				}
			}
			if len(panels) > 0 {
				v.Panels = panels
			}
			parseRatio := func(key string, fallback float64) float64 {
				percent, err := strconv.Atoi(strings.TrimSuffix(get(key), "%"))
				if err != nil {
					return fallback
				}
				return float64(percent) / 100
			}
			v.TopRatio = parseRatio("topRatio", v.TopRatio)
			v.LeftRatio = parseRatio("leftRatio", v.LeftRatio)
			v.UsageRatio = parseRatio("usageRatio", v.UsageRatio)
			m.upsertUserView(config.NormalizeView(v))
		}

		if err := m.saveDashboardConfig(); err != nil {
			m.footer = "save config: " + err.Error()
		} else {
			_ = m.st.Save()
			m.footer = "settings saved"
		}
		// Live palette swap.
		pal, warns := theme.LoadFromSettings(m.cfg.Settings)
		m.palette = pal
		if len(warns) > 0 {
			m.footer = warns[0].Message
		}

	case strings.HasPrefix(m.ov.formFor, "meter:"):
		// meter:<provider>:<editName>
		rest := strings.TrimPrefix(m.ov.formFor, "meter:")
		provName, editName, _ := strings.Cut(rest, ":")
		p := m.cfg.Find(provName)
		if p == nil {
			m.footer = "provider gone"
			break
		}
		meter := config.Meter{
			Name: strings.TrimSpace(get("name")),
			Unit: strings.TrimSpace(get("unit")),
			Kind: "manual",
		}
		meter.Used, _ = strconv.ParseFloat(strings.TrimSpace(get("used")), 64)
		meter.Cap, _ = strconv.ParseFloat(strings.TrimSpace(get("cap")), 64)
		rk := get("reset")
		rd := strings.TrimSpace(get("resetArg"))
		switch rk {
		case "monthly", "weekly", "date":
			if rd == "" {
				rd = "1"
			}
			meter.Reset = rk + ":" + rd
		default:
			meter.Reset = "never"
		}
		if editName == "" {
			p.Meters = append(p.Meters, meter)
		} else {
			for i := range p.Meters {
				if p.Meters[i].Name == editName {
					p.Meters[i] = meter
				}
			}
		}
		if err := config.Save(m.cfg); err != nil {
			m.footer = "save config: " + err.Error()
		} else {
			m.footer = "meter saved: " + meter.Name
		}
	}
	m.ov = overlayState{}
	return m, nil
}

// upsertUserView replaces or appends a user view by name.
func (m *model) upsertUserView(v config.View) {
	for i := range m.cfg.Views {
		if m.cfg.Views[i].Name == v.Name {
			m.cfg.Views[i] = v
			return
		}
	}
	m.cfg.Views = append(m.cfg.Views, v)
}

func enableDisableLabel(p *config.Provider) string {
	if p != nil && p.Enabled {
		return "disable"
	}
	return "enable"
}

func (m model) newIntegrationsOverlay() overlayState {
	return m.newIntegrationsOverlayWith(nil)
}

func (m model) newIntegrationsOverlayWith(status *moshiLocalStatus) overlayState {
	content := m.integrationsText(status)
	rendered, err := glamour.Render(content, "dark")
	if err != nil {
		rendered = content
	}
	vp := viewport.New(viewport.WithWidth(m.overlayWidth()), viewport.WithHeight(m.overlayHeight()))
	vp.SetContent(rendered)
	return overlayState{kind: overlayViewport, title: "integrations", vp: vp}
}

func (m model) integrationsText(status *moshiLocalStatus) string {
	addr := m.cfg.Settings.ServeListen
	if addr == "" {
		addr = "127.0.0.1:19777"
	}
	autoMeters := 0
	for _, p := range m.cfg.Providers {
		for _, mt := range p.Meters {
			if mt.Kind == "auto" && mt.Auto != "" {
				autoMeters++
			}
		}
	}
	moshiSection := "## Moshi daemon and hooks\n\n- status: checking local moshi-hook…"
	if status != nil {
		moshiSection = formatMoshiStatus(*status)
	}
	return fmt.Sprintf(`# integrations

Every dashboard snapshot is available to scripts and plugin surfaces.

%s

Controls: r refresh status · f repair stale hooks (confirmation required)
## Moshi setup

1. Moshi iPhone: Settings → Integrations → create pairing token.
2. Pair: `+"`moshi-hook pair --token '<token>' --name '<host>'`"+`
3. Start daemon: `+"`moshi-hook service install`"+`
4. Install hooks: `+"`moshi-hook install`"+` or `+"`moshi-hook install --target claude,codex,omp`"+`
5. Verify: `+"`moshi-hook probe`"+` and `+"`moshi-hook status`"+`

Status-slug reads these states only; it does not modify Moshi or agent hooks.

## Status-slug outputs

- Loopback HTTP: %s
  - start with %s
  - GET %s and %s
- Moshi usage payload: %s
- tmux/status lines: %s
- JSON: %s
- Doctor: %s

Auto meters configured: %d
`, moshiSection, "`"+addr+"`", "`sslug serve`", "`/status.json`", "`/usage.json`",
		"`sslug usage --format moshi`", "`sslug status --format tmux`",
		"`sslug status --format json`", "`sslug doctor`", autoMeters)
}

// --- help & inspect (glamour + viewport) ---

func (m model) newHelpOverlay() overlayState {
	rendered, err := glamour.Render(helpMarkdown, "dark")
	if err != nil {
		rendered = helpMarkdown
	}
	vp := viewport.New(viewport.WithWidth(m.overlayWidth()), viewport.WithHeight(m.overlayHeight()))
	vp.SetContent(rendered)
	return overlayState{kind: overlayViewport, title: "help", vp: vp}
}

func (m model) newInspectOverlay() overlayState {
	content := m.inspectText()
	content += "\n\n---\n\n`enter` re-probe · `esc` close"
	rendered, err := glamour.Render(content, "dark")
	if err != nil {
		rendered = content
	}
	vp := viewport.New(viewport.WithWidth(m.overlayWidth()), viewport.WithHeight(m.overlayHeight()))
	vp.SetContent(rendered)
	return overlayState{kind: overlayViewport, title: "inspect", vp: vp}
}

func (m model) overlayWidth() int {
	w := m.width * 2 / 3
	if w < 50 {
		w = 50
	}
	if w > 100 {
		w = 100
	}
	return w
}

func (m model) overlayHeight() int {
	h := m.height * 2 / 3
	if h < 10 {
		h = 10
	}
	return h
}

// inspectText builds the markdown for the inspect overlay.
func (m model) inspectText() string {
	var sb strings.Builder
	switch m.focused {
	case panelStatus:
		p := m.selectedProvider()
		if p == nil {
			return "no provider selected"
		}
		fmt.Fprintf(&sb, "# %s\n\n", p.Name)
		fmt.Fprintf(&sb, "- kind: `%s`\n- base: `%s`\n- key: `%s`\n- enabled: %v\n",
			p.Kind, p.BaseURL, secret.Redact(p.KeyRef), p.Enabled)
		if p.Note != "" {
			fmt.Fprintf(&sb, "- note: %s\n", p.Note)
		}
		if ps := m.st.Providers[p.Name]; ps != nil {
			if lc := ps.LastCheck; lc != nil {
				fmt.Fprintf(&sb, "\n## last check\n\n- status: **%s**\n- reason: %s\n- http: %d\n- latency: %.0fms\n- at: %s\n",
					lc.Status, lc.Reason, lc.HTTPCode, lc.LatencyMs,
					lc.CheckedAt.Format("2006-01-02 15:04:05 MST"))
			}
			if len(ps.Ring) > 0 {
				fmt.Fprintf(&sb, "\n## latency\n\np50 %.0fms · p95 %.0fms · n=%d\n",
					percentile(ps.Ring, 50), percentile(ps.Ring, 95), len(ps.Ring))
			}
			if len(ps.RecentErrors) > 0 {
				sb.WriteString("\n## recent errors\n\n")
				for _, e := range ps.RecentErrors {
					fmt.Fprintf(&sb, "- %s — %s (%s)\n",
						e.CheckedAt.Format("15:04"), e.Reason, e.Status)
				}
			}
		}
	case panelFavourites:
		pv, mod := m.selectedFavourite()
		if mod == nil {
			return "no favourite selected"
		}
		fmt.Fprintf(&sb, "# %s/%s\n\n", pv.Name, mod.ID)
		fmt.Fprintf(&sb, "- probe: `%s`\n", mod.Probe)
		if ps := m.st.Providers[pv.Name]; ps != nil {
			if ms := ps.Models[mod.ID]; ms != nil {
				if lc := ms.LastCheck; lc != nil {
					fmt.Fprintf(&sb, "- last: **%s** %.0fms %s\n", lc.Status, lc.LatencyMs,
						state.RelAge(time.Since(lc.CheckedAt)))
				}
				if len(ms.RecentErrors) > 0 {
					sb.WriteString("\n## recent errors\n\n")
					for _, e := range ms.RecentErrors {
						fmt.Fprintf(&sb, "- %s — %s (%s)\n",
							e.CheckedAt.Format("15:04"), e.Reason, e.Status)
					}
				}
			}
		}
	default:
		return "inspect: select a status or favourites row"
	}
	return sb.String()
}

// --- overlay rendering ---

func menuDescription(action string) string {
	switch {
	case strings.Contains(action, "theme"):
		return "Preview the active color theme."
	case strings.Contains(action, "view"):
		return "Change the progressive pane layout."
	case strings.Contains(action, "panel-order"):
		return "Rearrange panel admission order."
	case strings.Contains(action, "status"):
		return "Status pane sorting, checks and providers."
	case strings.Contains(action, "usage"):
		return "Usage meters, refresh and values."
	case strings.Contains(action, "fav"):
		return "Favourite models, probes and sorting."
	case strings.Contains(action, "stats"):
		return "Stats rows, sorting and counters."
	case strings.Contains(action, "integration"):
		return "Moshi daemon, hooks and output seams."
	case strings.Contains(action, "settings"):
		return "All global dashboard settings."
	case strings.Contains(action, "add"):
		return "Add a provider or local integration."
	case strings.Contains(action, "help"):
		return "Keys and interaction reference."
	case strings.Contains(action, "quit"):
		return "Exit status-slug."
	}
	return ""
}

func (m model) menuWindow() (start, end int) {
	visible := min(len(m.ov.menuItems), max(1, (m.height-6)/2))
	start = max(0, min(m.ov.menuSel-visible/2, len(m.ov.menuItems)-visible))
	return start, start + visible
}

func (m model) menuWidths() (left, right int) {
	available := max(24, m.width-6)
	left = min(29, max(14, available*2/5))
	return left, max(10, available-left-1)
}

func (m model) menuCategoryHeader(width int) string {
	action := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
	normal := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	word := func(before, key, after string) string {
		return normal.Render(before) + action.Render(key) + normal.Render(after)
	}
	categories := []string{
		word("", "m", "enu"),
		word("", "s", "tatus"),
		word("", "u", "sage"),
		word("", "f", "avourites"),
		word("s", "t", "ats"),
		word("inte", "g", "rations"),
		word("", "o", "ptions"),
	}
	return fitCells(strings.Join(categories, "  "), width)
}

func (m model) renderOverlay(base string) string {
	var content string
	switch m.ov.kind {
	case overlayConfirm:
		content = m.ov.title
		if m.ov.body != "" {
			content += "\n\n" + m.ov.body
		}
		content += "\n\ny / n"
	case overlayViewport:
		content = m.ov.vp.View()
	case overlayMenu:
		leftWidth, rightWidth := m.menuWidths()
		normal := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
		selected := lipgloss.NewStyle().
			Background(lipgloss.Color(m.palette[theme.SelectedBg])).
			Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
			Bold(true)
		divider := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.BoxBorder])).Render("│")
		var builder strings.Builder
		builder.WriteString(m.menuCategoryHeader(leftWidth + rightWidth + 1))
		builder.WriteString("\n" + muted.Render(strings.Repeat("─", leftWidth)) + divider + muted.Render(strings.Repeat("─", rightWidth)) + "\n")
		start, end := m.menuWindow()
		for index := start; index < end; index++ {
			item := m.ov.menuItems[index]
			label, value := item.label, ""
			if before, after, ok := strings.Cut(item.label, "  ‹ "); ok {
				label, value = before, "‹ "+after
			}
			leftTop := fitCells(label, leftWidth)
			leftBottom := fitCells(value, leftWidth)
			description := menuDescription(item.action)
			rightTop := fitCells(description, rightWidth)
			rightBottom := strings.Repeat(" ", rightWidth)
			if index == m.ov.menuSel {
				leftTop, leftBottom = selected.Render(leftTop), selected.Render(leftBottom)
				rightTop = normal.Render(rightTop)
			} else {
				leftTop, leftBottom = normal.Render(leftTop), muted.Render(leftBottom)
				rightTop = muted.Render(rightTop)
			}
			builder.WriteString(leftTop + divider + rightTop + "\n" + leftBottom + divider + rightBottom + "\n")
		}
		action := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
		hint := action.Render("j/k") + " navigate · " + action.Render("h/l") + " change · " + action.Render("enter") + " select · " + action.Render("esc") + " close"
		builder.WriteString(fitCells(hint, leftWidth+rightWidth+1))
		content = builder.String()
	case overlayInput:
		content = m.ov.input.View(m.overlayFormWidth()) + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).
				Render("enter submit · esc cancel")
	case overlayForm:
		content = m.ov.form.View(m.overlayFormWidth(), m.overlayHeight()-2)
	}

	title := ""
	if m.ov.title != "" {
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
		titleText := titleStyle.Render(m.ov.title)
		if m.ov.title == "menu" || strings.HasSuffix(m.ov.title, " menu") {
			action := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent])).Bold(true)
			prefix := strings.TrimSuffix(m.ov.title, "menu")
			titleText = titleStyle.Render(prefix) + action.Render("m") + titleStyle.Render("enu")
		}
		title = " " + titleText + " "
	}
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.palette[theme.BoxBorderFocus])).
		Padding(0, 1)
	if m.palette[theme.Bg] != "" {
		boxStyle = boxStyle.Background(lipgloss.Color(m.palette[theme.Bg]))
	}
	return compositeCentered(base, boxStyle.Render(content), m.width, m.height, title)
}

const helpMarkdown = `# sslug keys

| key | action |
|---|---|
| m | main menu |
| tab / shift-tab | cycle pane focus |
| j / k, PgUp/PgDn | scroll |
| c | check all providers |
| enter | check selected |
| i | inspect selected row |
| s / u / f / t | pane menus |
| p / shift+p | next / previous view preset |
| e | cycle themes (live) |
| g | integrations |
| o | settings |
| a | add provider |
| r | refresh Moshi status |
| d | remove selected provider |
| z | zoom pane |
| ? | this help |
| q | quit |

## integrations

- sslug serve exposes GET /status.json and /usage.json on loopback.
- sslug usage --format moshi emits moshi-hook usage snapshots.
- sslug status --format tmux feeds status lines; --format json feeds scripts.
- sslug doctor checks config, keys, meters, and integration settings.

## config

` + "`sslug config path`" + ` prints the config file.
See README for the full schema.
`
