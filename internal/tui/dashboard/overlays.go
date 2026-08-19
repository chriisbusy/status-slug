package dashboard

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
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
	input     textinput.Model // input
	inputFor  string          // what the input edits
	form      *huh.Form       // form
	formFor   string          // "settings" | "meter-add" | "meter-edit:<name>"
	vp        viewport.Model  // viewport
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

// forwardToOverlay routes non-key messages (spinner ticks, window events)
// to the active overlay component.
func (m model) forwardToOverlay(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.ov.kind {
	case overlayForm:
		f, cmd := m.ov.form.Update(msg)
		if hf, ok := f.(*huh.Form); ok {
			m.ov.form = hf
		}
		if m.ov.form.State == huh.StateCompleted {
			return m.completeForm()
		}
		if m.ov.form.State == huh.StateAborted {
			m.ov = overlayState{}
			return m, nil
		}
		return m, cmd
	case overlayViewport:
		var cmd tea.Cmd
		m.ov.vp, cmd = m.ov.vp.Update(msg)
		return m, cmd
	case overlayInput:
		var cmd tea.Cmd
		m.ov.input, cmd = m.ov.input.Update(msg)
		return m, cmd
	}
	return m, nil
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
		m.ov = newInputOverlay("custom model id", "fav-custom", "e.g. gpt-5-mini")
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

func newInputOverlay(title, inputFor, placeholder string) overlayState {
	ti := textinput.New()
	ti.Placeholder = placeholder
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
	m.ov = newInputOverlay(
		fmt.Sprintf("set %s/%s (%s)", entry.provider, entry.meter.Name, entry.meter.Unit),
		"meter:"+entry.provider+"/"+entry.meter.Name,
		"current value")
	return nil
}

func (m model) inputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.ov = overlayState{}
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.ov.input.Value())
		for_ := m.ov.inputFor
		m.ov = overlayState{}
		return m.handleInputSubmit(for_, val)
	}
	var cmd tea.Cmd
	m.ov.input, cmd = m.ov.input.Update(msg)
	return m, cmd
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

// --- meter form (huh) ---

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

	var (
		name, unit, usedStr, capStr string
		reset                       = "never"
		resetDay                    string
	)
	unit = "USD"
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

	resetOptions := []huh.Option[string]{
		huh.NewOption("never", "never"),
		huh.NewOption("monthly:<day>", "monthly"),
		huh.NewOption("weekly:<weekday>", "weekly"),
		huh.NewOption("date:<YYYY-MM-DD>", "date"),
	}
	// Preselect reset kind.
	resetKind := "never"
	if k, _, ok := strings.Cut(reset, ":"); ok {
		resetKind = k
		if k == "monthly" || k == "weekly" || k == "date" {
			_, resetDay, _ = strings.Cut(reset, ":")
		}
	}

	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("meter name").Value(&name).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("name required")
				}
				return nil
			}),
		huh.NewInput().Title("unit").Value(&unit),
		huh.NewInput().Title("current value (blank = 0)").Value(&usedStr),
		huh.NewInput().Title("cap (blank = uncapped)").Value(&capStr),
		huh.NewSelect[string]().Title("reset").Options(resetOptions...).Value(&resetKind),
		huh.NewInput().Title("reset argument (day 1-31 / mon..sun / YYYY-MM-DD)").Value(&resetDay),
	)).WithWidth(60).WithShowHelp(true)

	m.ov = overlayState{
		kind:    overlayForm,
		title:   "meter: " + p.Name,
		form:    form,
		formFor: "meter:" + p.Name + ":" + editName,
	}
	// Stash field pointers via closure on the model for completeForm.
	m.ov.body = "" // unused
	// We need the pointers at completion time; store them in a package-level
	// side channel keyed by form pointer is ugly — instead we read values via
	// huh's GetString with keys. Set keys:
	// (huh fields take .Key(...); simpler: capture pointers in a closure stored
	// on the overlay state.)
	m.ovFields = map[string]*string{
		"name": &name, "unit": &unit, "used": &usedStr,
		"cap": &capStr, "resetKind": &resetKind, "resetDay": &resetDay,
	}
	return m.ov.form.Init()
}

// --- settings form (huh) ---

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

	themeOpts := make([]huh.Option[string], len(themeNames))
	for i, n := range themeNames {
		themeOpts[i] = huh.NewOption(n, n)
	}

	viewNames := m.viewCycleOrder()
	viewOpts := make([]huh.Option[string], len(viewNames))
	for i, n := range viewNames {
		viewOpts[i] = huh.NewOption(n, n)
	}

	splitOpts := []huh.Option[string]{}
	for pct := 40; pct <= 80; pct += 5 {
		splitOpts = append(splitOpts, huh.NewOption(fmt.Sprintf("%d%%", pct), strconv.Itoa(pct)))
	}
	curSplit := strconv.Itoa(int(s2split(m.activeViewDef().MainSplit) * 100))

	var (
		themeSel   = s.Theme
		viewSel    = m.activeViewDef().Name
		arrSel     = m.activeViewDef().Arrangement
		compactSel = m.activeViewDef().Compact
		splitSel   = curSplit
		borderSel  = s.BorderStyle
		glyphSel   = s.GraphGlyphs
		timeoutIn  = strconv.Itoa(s.ProbeTimeout)
		refreshSel = strconv.Itoa(s.AutoRefresh)
		modeSel    = s.ProbeMode
		histIn     = strconv.Itoa(s.HistoryLength)
		keysSel    = s.KeysSource
		nerdSel    = s.NerdFont
		quitSel    = s.ConfirmQuit
		launchSel  = s.CheckOnLaunch
		bellSel    = s.AlertBell
	)
	if themeSel == "" {
		themeSel = "sstop"
	}

	panelToggles := map[string]bool{}
	for _, n := range panelNames {
		panelToggles[n] = false
	}
	for _, n := range m.activeViewDef().Panels {
		panelToggles[n] = true
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("theme").Options(themeOpts...).Value(&themeSel),
			huh.NewSelect[string]().Title("view").Options(viewOpts...).Value(&viewSel),
			huh.NewSelect[string]().Title("arrangement").Options(
				huh.NewOption("grid", "grid"), huh.NewOption("stack", "stack")).Value(&arrSel),
			huh.NewConfirm().Title("compact density").Value(&compactSel),
			huh.NewSelect[string]().Title("main split").Options(splitOpts...).Value(&splitSel),
			huh.NewSelect[string]().Title("border style").Options(
				huh.NewOption("rounded", "rounded"), huh.NewOption("square", "square"),
				huh.NewOption("thick", "thick")).Value(&borderSel),
			huh.NewSelect[string]().Title("graph glyphs").Options(
				huh.NewOption("braille", "braille"), huh.NewOption("blocks", "blocks"),
				huh.NewOption("ascii", "ascii")).Value(&glyphSel),
		),
		huh.NewGroup(
			huh.NewInput().Title("probe timeout (5-30s)").Value(&timeoutIn).
				Validate(intRange(5, 30)),
			huh.NewSelect[string]().Title("auto refresh").Options(
				huh.NewOption("off", "0"), huh.NewOption("30s", "30"),
				huh.NewOption("60s", "60"), huh.NewOption("300s", "300")).Value(&refreshSel),
			huh.NewSelect[string]().Title("probe mode").Options(
				huh.NewOption("models", "models"), huh.NewOption("chat", "chat")).Value(&modeSel),
			huh.NewInput().Title("history length (20-240)").Value(&histIn).
				Validate(intRange(20, 240)),
			huh.NewSelect[string]().Title("keys source").Options(
				huh.NewOption("auto", "auto"), huh.NewOption("keyring", "keyring"),
				huh.NewOption("file", "file"), huh.NewOption("env", "env")).Value(&keysSel),
		),
		huh.NewGroup(
			huh.NewConfirm().Title("nerd font glyphs").Value(&nerdSel),
			huh.NewConfirm().Title("confirm quit").Value(&quitSel),
			huh.NewConfirm().Title("check on launch").Value(&launchSel),
			huh.NewConfirm().Title("alert bell on down").Value(&bellSel),
			huh.NewConfirm().Title("panel: status").Value(mapBool(panelToggles, "status")),
			huh.NewConfirm().Title("panel: usage").Value(mapBool(panelToggles, "usage")),
			huh.NewConfirm().Title("panel: favourites").Value(mapBool(panelToggles, "favourites")),
			huh.NewConfirm().Title("panel: stats").Value(mapBool(panelToggles, "stats")),
		),
	).WithWidth(64)

	ov := overlayState{
		kind:    overlayForm,
		title:   "settings",
		form:    form,
		formFor: "settings",
	}
	m.ovFields = map[string]*string{
		"theme": &themeSel, "view": &viewSel, "arrangement": &arrSel,
		"split": &splitSel, "border": &borderSel, "glyphs": &glyphSel,
		"timeout": &timeoutIn, "refresh": &refreshSel, "mode": &modeSel,
		"history": &histIn, "keys": &keysSel,
	}
	m.ovBoolFields = map[string]*bool{
		"compact": &compactSel, "nerd": &nerdSel, "confirmQuit": &quitSel,
		"checkOnLaunch": &launchSel, "alertBell": &bellSel,
		"panel:status":     mapBool(panelToggles, "status"),
		"panel:usage":      mapBool(panelToggles, "usage"),
		"panel:favourites": mapBool(panelToggles, "favourites"),
		"panel:stats":      mapBool(panelToggles, "stats"),
	}
	m.ov = ov
	return m.ov
}

func s2split(f float64) float64 {
	if f < 0.4 || f > 0.8 {
		return 0.66
	}
	return f
}

func mapBool(m map[string]bool, k string) *bool {
	v := m[k]
	return &v
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

func (m model) formKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		m.ov = overlayState{}
		return m, nil
	}
	f, cmd := m.ov.form.Update(msg)
	if hf, ok := f.(*huh.Form); ok {
		m.ov.form = hf
	}
	if m.ov.form.State == huh.StateCompleted {
		return m.completeForm()
	}
	if m.ov.form.State == huh.StateAborted {
		m.ov = overlayState{}
		return m, nil
	}
	return m, cmd
}

// completeForm applies a completed huh form.
func (m model) completeForm() (tea.Model, tea.Cmd) {
	switch {
	case m.ov.formFor == "settings":
		s := &m.cfg.Settings
		get := func(k string) string { return *m.ovFields[k] }
		getb := func(k string) bool { return *m.ovBoolFields[k] }

		s.Theme = get("theme")
		s.BorderStyle = get("border")
		s.GraphGlyphs = get("glyphs")
		s.ProbeTimeout, _ = strconv.Atoi(get("timeout"))
		s.AutoRefresh, _ = strconv.Atoi(get("refresh"))
		s.ProbeMode = get("mode")
		s.HistoryLength, _ = strconv.Atoi(get("history"))
		s.KeysSource = get("keys")
		s.NerdFont = getb("nerd")
		s.ConfirmQuit = getb("confirmQuit")
		s.CheckOnLaunch = getb("checkOnLaunch")
		s.AlertBell = getb("alertBell")

		// Apply view changes: materialize into user views if builtin.
		viewName := get("view")
		v := m.activeViewDef()
		if v.Name != viewName {
			m.st.UI.View = viewName
		} else {
			// Edit panels/arrangement/compact/split on the active view.
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
			if pct, err := strconv.Atoi(get("split")); err == nil {
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
		get := func(k string) string { return *m.ovFields[k] }
		meter := config.Meter{
			Name: strings.TrimSpace(get("name")),
			Unit: strings.TrimSpace(get("unit")),
			Kind: "manual",
		}
		meter.Used, _ = strconv.ParseFloat(strings.TrimSpace(get("used")), 64)
		meter.Cap, _ = strconv.ParseFloat(strings.TrimSpace(get("cap")), 64)
		rk := get("resetKind")
		rd := strings.TrimSpace(get("resetDay"))
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
	m.ovFields = nil
	m.ovBoolFields = nil
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
			if i == m.ov.menuSel {
				b.WriteString(lipgloss.NewStyle().
					Background(lipgloss.Color(m.palette[theme.SelectedBg])).
					Foreground(lipgloss.Color(m.palette[theme.SelectedFg])).
					Render("> " + item.label))
			} else {
				b.WriteString("  " + item.label)
			}
			b.WriteString("\n")
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).
			Render("\nenter select · esc close"))
		content = b.String()
	case overlayInput:
		content = m.ov.input.View() + "\n\n" +
			lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).
				Render("enter submit · esc cancel")
	case overlayForm:
		content = m.ov.form.View()
	}

	title := ""
	if m.ov.title != "" {
		title = " " + m.ov.title + " "
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.palette[theme.BoxBorderFocus])).
		Background(lipgloss.Color(m.palette[theme.Bg])).
		Padding(0, 1).
		Render(content)

	// Center on base frame.
	baseLines := strings.Split(base, "\n")
	ovLines := strings.Split(box, "\n")
	ovW := 0
	for _, l := range ovLines {
		if w := lipgloss.Width(l); w > ovW {
			ovW = w
		}
	}
	startY := (len(baseLines) - len(ovLines)) / 2
	if startY < 0 {
		startY = 0
	}
	startX := (m.width - ovW) / 2
	if startX < 0 {
		startX = 0
	}
	for i, ol := range ovLines {
		y := startY + i
		if y >= len(baseLines) {
			break
		}
		baseLine := []rune(baseLines[y])
		// Pad base line to startX.
		for len(baseLine) < startX {
			baseLine = append(baseLine, ' ')
		}
		olRunes := []rune(ol)
		end := startX + len(olRunes)
		for len(baseLine) < end {
			baseLine = append(baseLine, ' ')
		}
		copy(baseLine[startX:end], olRunes)
		baseLines[y] = string(baseLine)
	}
	// Title on the overlay's top border.
	if title != "" && startY < len(baseLines) {
		bl := []rune(baseLines[startY])
		tr := []rune(title)
		pos := startX + 2
		if pos+len(tr) <= len(bl) {
			copy(bl[pos:pos+len(tr)], tr)
			baseLines[startY] = string(bl)
		}
	}
	return strings.Join(baseLines, "\n")
}

const helpMarkdown = `# sslug keys

| key | action |
|---|---|
| tab / shift-tab | cycle pane focus |
| j / k, PgUp/PgDn | scroll |
| c | check all providers |
| enter | check selected |
| i | inspect selected row |
| s / u / f / t | pane menus |
| p | cycle view presets |
| S | settings |
| a | add provider (exits to wizard) |
| d | remove selected provider |
| z | zoom pane |
| ? | this help |
| q | quit |

## config

` + "`sslug config path`" + ` prints the config file.
See README for the full schema.
`
