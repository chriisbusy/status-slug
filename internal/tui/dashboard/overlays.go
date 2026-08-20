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
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return dashBlinkMsg{} })
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

func (m model) newMenuOverlay(p panelID) overlayState {
	ov := overlayState{kind: overlayMenu, title: panelNames[p] + " menu"}
	switch p {
	case panelStatus:
		ov.menuItems = []menuItem{
			{"sort: name", "status.sort:name"},
			{"sort: status", "status.sort:status"},
			{"sort: latency", "status.sort:latency"},
			{"sort: last checked", "status.sort:checked"},
			{fmt.Sprintf("group by label: %s", onOff(m.prefs.statusGroup)), "status.group"},
			{"check selected", "status.check"},
			{"edit selected provider", "status.edit"},
			{fmt.Sprintf("%s selected provider", enableDisableLabel(m.selectedProvider())), "status.toggle-enabled"},
			{"remove selected provider", "status.remove"},
		}
	case panelUsage:
		ov.menuItems = []menuItem{
			{"set meter value", "usage.set"},
			{"refresh auto meters", "usage.refresh"},
			{"add meter", "usage.add"},
			{"edit meter", "usage.edit"},
			{"remove meter", "usage.remove"},
		}
	case panelFavourites:
		ov.menuItems = []menuItem{
			{"add favourite (from known models)", "fav.add-known"},
			{"add favourite (custom id)", "fav.add-custom"},
			{"toggle probe mode (chat/models) on selected", "fav.toggle-probe"},
			{"remove selected favourite", "fav.remove"},
			{"re-probe selected", "fav.reprobe"},
			{"sort: name", "fav.sort:name"},
			{"sort: latency", "fav.sort:latency"},
			{"sort: status", "fav.sort:status"},
		}
	case panelStats:
		ov.menuItems = []menuItem{
			{"sort by column", "stats.sort"},
			{fmt.Sprintf("favourite rows: %s", onOff(m.prefs.statsShowFavs)), "stats.favs"},
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

func (m model) menuKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
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
	case "enter":
		if m.ov.menuSel >= len(m.ov.menuItems) {
			m.ov = overlayState{}
			return m, nil
		}
		action := m.ov.menuItems[m.ov.menuSel].action
		return m.runMenuAction(action)
	}
	return m, nil
}

// runMenuAction executes a menu action id.
func (m model) runMenuAction(action string) (tea.Model, tea.Cmd) {
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
		case "theme":
			return m.cycleTheme(), nil
		case "view":
			return m.cycleView(), nil
		case "integrations":
			m.ov = m.newIntegrationsOverlay()
		case "help":
			m.ov = m.newHelpOverlay()
		case "quit":
			return m, tea.Quit
		}

	case strings.HasPrefix(action, "status.sort:"):
		m.prefs.statusSort = strings.TrimPrefix(action, "status.sort:")
		m.savePrefs()
	case action == "status.group":
		m.prefs.statusGroup = !m.prefs.statusGroup
		m.savePrefs()
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
		return m, m.openSetValueInput()
	case action == "usage.refresh":
		if !m.checking {
			cmd := m.startCheckAll() // auto meters refresh on check
			return m, cmd
		}
	case action == "usage.add":
		return m, m.openMeterForm("")
	case action == "usage.edit":
		// Edit the meter under the usage selection.
		if entry := m.selectedMeterEntry(); entry != nil && entry.meter != nil {
			return m, m.openMeterForm(entry.meter.Name)
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
			{"name", "stats.sortcol:name"},
			{"checks", "stats.sortcol:checks"},
			{"ok%", "stats.sortcol:ok%"},
			{"p50", "stats.sortcol:p50"},
			{"p95", "stats.sortcol:p95"},
			{"down", "stats.sortcol:down"},
			{"ago", "stats.sortcol:ago"},
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
func (m model) openSetValueInput() tea.Cmd {
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
		return m, m.syncBars()
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

func (m model) openMeterForm(editName string) tea.Cmd {
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
	selectIdx(viewF, viewNames, m.activeViewDef().Name)

	arrF := widgets.NewSelect(m.palette, "arrangement", []string{"grid", "stack"})
	selectIdx(arrF, []string{"grid", "stack"}, m.activeViewDef().Arrangement)

	compactF := widgets.NewConfirm(m.palette, "compact density", m.activeViewDef().Compact)

	var splitOpts []string
	for pct := 40; pct <= 80; pct += 5 {
		splitOpts = append(splitOpts, fmt.Sprintf("%d%%", pct))
	}
	splitF := widgets.NewSelect(m.palette, "main split", splitOpts)
	selectIdx(splitF, splitOpts, fmt.Sprintf("%d%%", int(s2split(m.activeViewDef().MainSplit)*100)))

	borderF := widgets.NewSelect(m.palette, "border style", []string{"rounded", "square", "thick"})
	selectIdx(borderF, []string{"rounded", "square", "thick"}, s.BorderStyle)

	glyphF := widgets.NewSelect(m.palette, "graph glyphs", []string{"braille", "blocks", "ascii"})
	selectIdx(glyphF, []string{"braille", "blocks", "ascii"}, s.GraphGlyphs)

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
		widgets.NewNote(m.palette, "appearance", "theme, layout, chrome"),
		themeF, viewF, arrF, compactF, splitF, borderF, glyphF, bgF,
		widgets.NewNote(m.palette, "probing", "timeouts, refresh, key storage"),
		timeoutF, refreshF, modeF, histF, keysF,
		widgets.NewNote(m.palette, "integrations", "loopback api for moshi, tmux, scripts"),
		serveF,
		widgets.NewNote(m.palette, "behavior & panels", "toggles and pane visibility"),
		nerdF, quitF, launchF, bellF, pStatusF, pUsageF, pFavF, pStatsF,
	}

	return overlayState{
		kind:  overlayForm,
		title: "settings",
		form:  widgets.NewForm(m.palette, "", "", fields...),
		fields: map[string]widgets.Field{
			"theme": themeF, "view": viewF, "arrangement": arrF, "compact": compactF,
			"split": splitF, "border": borderF, "glyphs": glyphF, "themeBackground": bgF,
			"timeout": timeoutF, "refresh": refreshF, "mode": modeF, "history": histF,
			"keys": keysF, "serveListen": serveF, "nerd": nerdF, "confirmQuit": quitF,
			"checkOnLaunch": launchF, "alertBell": bellF,
			"panel:status": pStatusF, "panel:usage": pUsageF,
			"panel:favourites": pFavF, "panel:stats": pStatsF,
		},
		formFor: "settings",
	}
}

func s2split(f float64) float64 {
	if f < 0.4 || f > 0.8 {
		return 0.66
	}
	return f
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
		s.GraphGlyphs = get("glyphs")
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

		// Apply view changes: materialize into user views if builtin.
		viewName := get("view")
		v := m.activeViewDef()
		if v.Name != viewName {
			m.st.UI.View = viewName
		} else {
			var panels []string
			for _, n := range panelNames {
				if getb("panel:" + n) {
					panels = append(panels, n)
				}
			}
			if len(panels) > 0 {
				v.Panels = panels
			}
			v.Arrangement = get("arrangement")
			v.Compact = getb("compact")
			if pct, err := strconv.Atoi(strings.TrimSuffix(get("split"), "%")); err == nil {
				v.MainSplit = float64(pct) / 100
			}
			m.upsertUserView(v)
		}

		if err := config.Save(m.cfg); err != nil {
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
	content := m.integrationsText()
	rendered, err := glamour.Render(content, "dark")
	if err != nil {
		rendered = content
	}
	vp := viewport.New(viewport.WithWidth(m.overlayWidth()), viewport.WithHeight(m.overlayHeight()))
	vp.SetContent(rendered)
	return overlayState{kind: overlayViewport, title: "integrations", vp: vp}
}

func (m model) integrationsText() string {
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
	return fmt.Sprintf(`# integrations

Every dashboard snapshot is available to scripts and plugin surfaces.

- Loopback HTTP: %s
  - start with %s
  - GET %s and %s
- Moshi/iOS: %s emits moshi-hook usage snapshots.
- tmux/status lines: %s gives compact dots.
- JSON: %s prints the full status contract.
- Doctor: %s checks config, keys, meters, and serve settings.

Auto meters configured: %d
`, "`"+addr+"`", "`sslug serve`", "`/status.json`", "`/usage.json`",
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
		var b strings.Builder
		for i, item := range m.ov.menuItems {
			label := item.label
			if item.action == "stats.sort" {
				label += " ›"
			}
			if i == m.ov.menuSel {
				b.WriteString(lipgloss.NewStyle().
					Background(lipgloss.Color(m.palette[theme.SelectedBg])).
					Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
					Render("> " + label))
			} else {
				b.WriteString("  " + label)
			}
			b.WriteString("\n")
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).
			Render("\nj/k navigate · enter select · esc close"))
		content = b.String()
	case overlayInput:
		content = m.ov.input.View(m.overlayFormWidth()) + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).
				Render("enter submit · esc cancel")
	case overlayForm:
		content = m.ov.form.View(m.overlayFormWidth(), m.overlayHeight()-2)
	}

	title := ""
	if m.ov.title != "" {
		title = " " + m.ov.title + " "
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
| p | cycle view presets |
| e | cycle themes (live) |
| g | integrations |
| o | settings |
| a | add provider |
| r | edit provider (wizard popup) |
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
