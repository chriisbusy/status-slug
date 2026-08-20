package wizard

// steps.go — per-step widget form builders for the wizard.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/theme"
	"github.com/chriisbusy/status-slug/internal/tui/widgets"
)

// --- step: identity ---

func (m *Model) enterIdentity() {
	m.step = stepIdentity
	d := m.data
	if d.label == "" {
		d.label = "official"
	}

	presetOpts := make([]string, 0, len(provider.Presets)+1)
	for _, p := range provider.Presets {
		presetOpts = append(presetOpts, p.Name)
	}
	presetOpts = append(presetOpts, "Custom")

	nameF := widgets.NewText(m.palette, "what do you call it?", "e.g. OpenAI, or Neuralwatt (homelab)")
	nameF.Value = d.name
	nameF.Validate = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("a name is required")
		}
		if m.reconfigure == "" && m.cfg.Find(strings.TrimSpace(s)) != nil {
			return fmt.Errorf("%q already exists", s)
		}
		return nil
	}

	labelF := widgets.NewSelect(m.palette, "how is it billed / provided?", []string{
		"official — first-party api",
		"third-party — reseller or gateway",
		"coding-plan — subscription bundle",
		"custom — something else entirely",
	})
	labelKeys := []string{"official", "third-party", "coding-plan", "custom"}
	for i, k := range labelKeys {
		if k == d.label {
			labelF.Selected = i
		}
	}

	presetF := widgets.NewSelect(m.palette, "which preset?", presetOpts)
	presetF.Hint = "presets fill in the endpoint and auth style; custom works with any openai-compatible api"
	for i, p := range provider.Presets {
		if p.Kind == d.kind && p.BaseURL == d.baseURL {
			presetF.Selected = i
		}
	}
	if d.kind == "custom" || (d.baseURL != "" && presetF.Selected == 0 && d.kind == "") {
		presetF.Selected = len(presetOpts) - 1
	}

	fields := []widgets.Field{
		widgets.NewNote(m.palette, "welcome to sslug",
			"let's add your first provider. pick a preset or go fully custom — sslug watches anything that speaks http."),
		nameF, labelF, presetF,
	}

	var urlF *widgets.TextField
	if presetF.Value() == "Custom" {
		urlF = widgets.NewText(m.palette, "base url", "https://…")
		urlF.Value = d.baseURL
		urlF.Validate = func(s string) error {
			if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
				return fmt.Errorf("must start with http:// or https://")
			}
			return nil
		}
		fields = append(fields, urlF)
	}

	m.form = widgets.NewForm(m.palette, "", "", fields...)
	m.harvest = func() {
		d.name = strings.TrimSpace(nameF.Value)
		d.label = labelKeys[labelF.Selected]
		d.presetSel = presetF.Value()
		if urlF != nil {
			d.baseURL = urlF.Value
		}
	}
	m.pendingDone = func() {
		if d.presetSel == "Custom" {
			d.kind = "custom"
		} else if p := provider.FindPreset(d.presetSel); p != nil {
			d.kind = p.Kind
			d.baseURL = p.BaseURL
		}
		m.gotoStep = stepKeySource
	}
}

// --- step: key source ---

func (m *Model) enterKeySource() (tea.Model, tea.Cmd) {
	m.step = stepKeySource
	d := m.data

	srcOpts := []string{
		"paste a key — stored in your os keyring",
		"read it from a file…",
	}
	srcKeys := []string{"paste", "locate"}
	for _, v := range DetectEnvVars(os.Environ()) {
		srcOpts = append(srcOpts, "use $"+v+" (detected)")
		srcKeys = append(srcKeys, "env:"+v)
	}
	srcOpts = append(srcOpts, "no key — local or unauthenticated")
	srcKeys = append(srcKeys, "none")

	srcF := widgets.NewSelect(m.palette, "where's the key?", srcOpts)
	m.form = widgets.NewForm(m.palette, "", "", []widgets.Field{
		widgets.NewNote(m.palette, "the key stays yours",
			"keys go to your os keyring and are never printed, logged, or sent anywhere except this provider's api. read-only probes only."),
		srcF,
	}...)
	m.harvest = func() { d.keySrc = srcKeys[srcF.Selected] }
	m.pendingDone = func() {
		switch {
		case d.keySrc == "none":
			d.keyRef, d.keyMaterial, _ = KeyRef("none", "", d.name)
			m.gotoStep = stepModels
		case strings.HasPrefix(d.keySrc, "env:"):
			v := strings.TrimPrefix(d.keySrc, "env:")
			d.keyRef, _, m.err = KeyRef("env", v, d.name)
			m.gotoStep = stepValidate
		default:
			m.gotoStep = stepKeyDetail
		}
	}
	return m, nil
}

// --- step: key detail (paste / locate / fallback) ---

func (m *Model) enterKeyDetail() (tea.Model, tea.Cmd) {
	m.step = stepKeyDetail
	d := m.data

	// Decide storage destination up front.
	dest := m.cfg.Settings.KeysSource
	keyringOK := secret.KeyringAvailable()
	needFallback := false
	switch dest {
	case "keyring":
		if !keyringOK {
			m.err = fmt.Errorf("keys_source=keyring but the os keyring is unavailable")
			return m, tea.Quit
		}
	case "file", "env":
		// forced; no fallback dialog
	default: // auto
		if keyringOK {
			dest = "keyring"
		} else {
			needFallback = true
		}
	}

	var fields []widgets.Field
	materialF := widgets.NewText(m.palette, "paste it in", "sk-…")
	pathF := widgets.NewText(m.palette, "path to the key file", "~/.config/provider/key")
	fallbackF := widgets.NewSelect(m.palette, "no os keyring here. store it how?", []string{
		"0600 file — plaintext at rest, like curl .netrc",
		"keep it in an env var instead",
		"abort",
	})
	envF := widgets.NewText(m.palette, "environment variable name", "MY_PROVIDER_API_KEY")

	switch {
	case d.keySrc == "paste" && !needFallback && dest != "env":
		materialF.Password = true
		fields = []widgets.Field{materialF}
	case d.keySrc == "paste" && needFallback:
		materialF.Password = true
		fields = []widgets.Field{materialF, fallbackF, envF}
	case d.keySrc == "paste" && dest == "env":
		materialF.Password = true
		fields = []widgets.Field{materialF, envF}
	case d.keySrc == "locate" && !needFallback && dest != "env":
		fields = []widgets.Field{pathF}
	case d.keySrc == "locate" && needFallback:
		fields = []widgets.Field{pathF, fallbackF, envF}
	case d.keySrc == "locate" && dest == "env":
		fields = []widgets.Field{pathF, envF}
	}

	m.form = widgets.NewForm(m.palette, "", "", fields...)
	m.harvest = func() {
		d.pastedKey = materialF.Value
		d.locatePath = pathF.Value
		d.envName = envF.Value
		if needFallback {
			d.fallbackChoice = []string{"file", "env", "abort"}[fallbackF.Selected]
		}
	}
	m.pendingDone = func() {
		material := d.pastedKey
		if d.keySrc == "locate" {
			data, err := os.ReadFile(d.locatePath)
			if err != nil {
				m.err = fmt.Errorf("read key file: %w", err)
				return
			}
			material = strings.TrimSpace(string(data))
			if material == "" {
				m.err = fmt.Errorf("key file %s is empty", d.locatePath)
				return
			}
		}
		if needFallback {
			switch d.fallbackChoice {
			case "abort":
				m.step = stepAborted
				return
			case "env":
				d.keyRef, _, m.err = KeyRef("env", d.envName, d.name)
				m.gotoStep = stepValidate
				return
			default:
				dest = "file"
			}
		}
		if dest == "env" {
			d.keyRef, _, m.err = KeyRef("env", d.envName, d.name)
		} else {
			d.keyRef, d.keyMaterial, m.err = KeyRef(dest, material, d.name)
		}
		m.gotoStep = stepValidate
	}
	return m, nil
}

// --- step: validate (async probe) ---

func (m *Model) enterValidate() (tea.Model, tea.Cmd) {
	if m.err != nil {
		return m, tea.Quit
	}
	m.step = stepValidate
	d := m.data
	if d.keyRef == "" || d.keyRef == "none" || d.baseURL == "" {
		return m.enterModelsFetch()
	}
	m.form = nil
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		key, _ := secret.Resolve(d.keyRef)
		if key == "" {
			key = d.keyMaterial
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		trial := config.Provider{Name: d.name, Kind: d.kind, BaseURL: d.baseURL}
		res := provider.New(d.kind).Probe(ctx, check.NewDoer(8*time.Second, key), trial)
		return validateResultMsg{result: res}
	})
}

func (m *Model) afterValidate() (tea.Model, tea.Cmd) {
	d := m.data
	if d.validation.Status == check.OK {
		m.gotoStep = stepModels
		return m.enterModelsFetch()
	}
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(statusColor(m.palette, string(d.validation.Status))))
	choiceF := widgets.NewSelect(m.palette, "what now?", []string{
		"save anyway — i'll fix it later",
		"re-enter the key",
		"abort setup",
	})
	m.form = widgets.NewForm(m.palette, "", "", []widgets.Field{
		widgets.NewNote(m.palette, "probe came back "+statusStyle.Render(string(d.validation.Status)), d.validation.Reason),
		choiceF,
	}...)
	m.harvest = func() {
		d.validateChoice = []string{"save", "retry", "abort"}[choiceF.Selected]
	}
	m.pendingDone = func() {
		switch d.validateChoice {
		case "retry":
			d.keyRef, d.keyMaterial = "", ""
			m.gotoStep = stepKeySource
		case "abort":
			m.step = stepAborted
		default:
			m.gotoStep = stepModels
		}
	}
	return m, nil
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

// --- step: models ---

func (m *Model) enterModelsFetch() (tea.Model, tea.Cmd) {
	m.step = stepModels
	d := m.data
	if d.baseURL == "" {
		return m.enterModelsForm()
	}
	m.form = nil
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		key, _ := secret.Resolve(d.keyRef)
		if key == "" {
			key = d.keyMaterial
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		doer := check.NewDoer(8*time.Second, key)
		ids, err := provider.ListModelsRaw(ctx, doer, d.kind, d.baseURL)
		if err != nil {
			return modelsResultMsg{errText: err.Error()}
		}
		return modelsResultMsg{ids: ids}
	})
}

func (m *Model) enterModelsForm() (tea.Model, tea.Cmd) {
	d := m.data
	m.step = stepModels

	var note *widgets.Note
	switch {
	case d.fetchErr != "":
		note = widgets.NewNote(m.palette, "couldn't fetch the model list",
			"add model ids by hand below — or finish now and the dashboard can fetch them later.")
	case len(d.discovered) == 0:
		note = widgets.NewNote(m.palette, "no models discovered",
			"add ids by hand below, or finish and pick them later.")
	default:
		note = widgets.NewNote(m.palette, fmt.Sprintf("%d models found", len(d.discovered)),
			"space toggles — favourites get latency probes and their own cockpit pane.")
	}

	multiF := widgets.NewMulti(m.palette, "favourites", d.discovered)
	customF := widgets.NewText(m.palette, "any other model id? (blank to skip)", "e.g. neuralwatt-large-v3")
	customF.Value = d.customModel

	fields := []widgets.Field{note}
	if len(d.discovered) > 0 {
		fields = append(fields, multiF)
	}
	fields = append(fields, customF)

	m.form = widgets.NewForm(m.palette, "", "", fields...)
	m.harvest = func() {
		d.selectedModels = multiF.Values()
		d.customModel = strings.TrimSpace(customF.Value)
	}
	m.pendingDone = func() {
		customList := []string{}
		if d.customModel != "" {
			customList = []string{d.customModel}
		}
		var existing []config.Model
		if p := m.cfg.Find(d.name); p != nil {
			existing = p.Models
		}
		d.models = MergeModels(d.selectedModels, customList, existing)
		for i := range d.models {
			for _, sel := range d.selectedModels {
				if d.models[i].ID == sel {
					d.models[i].Favourite = true
				}
			}
		}
		m.gotoStep = stepMeters
	}
	return m, nil
}

// --- step: meters ---

func (m *Model) enterMeters() (tea.Model, tea.Cmd) {
	m.step = stepMeters
	d := m.data
	d.addMeter = false

	addF := widgets.NewConfirm(m.palette, "add a usage meter?", false)
	fields := []widgets.Field{
		widgets.NewNote(m.palette, "usage meters — track anything",
			"energy, spend, requests, credits — any unit, with a cap and a reset cycle. update them from scripts with `sslug usage set`. skip freely: the dashboard shows probes either way."),
		addF,
	}
	var creditsF *widgets.ConfirmField
	if strings.Contains(d.baseURL, "openrouter.ai") {
		creditsF = widgets.NewConfirm(m.palette, "openrouter detected — attach the auto credits meter?", d.attachCredits)
		creditsF.Hint = "fetched live from the openrouter credits api on every check."
		fields = append(fields, creditsF)
	}

	m.form = widgets.NewForm(m.palette, "", "", fields...)
	m.harvest = func() {
		d.addMeter = addF.Value
		if creditsF != nil {
			d.attachCredits = creditsF.Value
		}
	}
	m.pendingDone = func() {
		if d.attachCredits {
			d.meters = append(d.meters, config.Meter{
				Name: "Credits", Unit: "USD", Kind: "auto",
				Auto: "openrouter-credits", Reset: "never",
			})
		}
		if d.addMeter {
			d.meterDraft = meterDraft{unit: "USD", resetKind: "never"}
			m.gotoStep = stepMeterForm
		} else {
			m.gotoStep = stepSummary
		}
	}
	return m, nil
}

// enterMeterForm runs one meter definition; loops while the user keeps adding.
func (m *Model) enterMeterForm() (tea.Model, tea.Cmd) {
	m.step = stepMeterForm
	md := &m.data.meterDraft

	nameF := widgets.NewText(m.palette, "meter name", "Energy, Spend, Requests…")
	nameF.Value = md.name
	nameF.Validate = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("name required")
		}
		return nil
	}
	unitF := widgets.NewText(m.palette, "unit", "USD")
	unitF.Value = md.unit
	usedF := widgets.NewText(m.palette, "current value (blank = 0)", "0")
	usedF.Value = md.used
	usedF.Validate = optionalFloat
	capF := widgets.NewText(m.palette, "cap (blank = no cap)", "1000")
	capF.Value = md.cap
	capF.Validate = optionalFloat
	resetF := widgets.NewSelect(m.palette, "resets", []string{
		"never",
		"monthly — on a day of the month",
		"weekly — on a weekday",
		"on a fixed date",
	})
	resetKeys := []string{"never", "monthly", "weekly", "date"}
	for i, k := range resetKeys {
		if k == md.resetKind {
			resetF.Selected = i
		}
	}
	argF := widgets.NewText(m.palette, "reset argument", "monthly: 1-31 · weekly: mon..sun · date: YYYY-MM-DD")
	argF.Value = md.resetArg

	m.form = widgets.NewForm(m.palette, "", "", []widgets.Field{nameF, unitF, usedF, capF, resetF, argF}...)
	m.harvest = func() {
		md.name = nameF.Value
		md.unit = unitF.Value
		md.used = usedF.Value
		md.cap = capF.Value
		md.resetKind = resetKeys[resetF.Selected]
		md.resetArg = argF.Value
	}
	m.pendingDone = func() {
		mt := config.Meter{Name: strings.TrimSpace(md.name), Unit: strings.TrimSpace(md.unit), Kind: "manual"}
		if s := strings.TrimSpace(md.used); s != "" {
			mt.Used, _ = strconv.ParseFloat(s, 64)
		}
		if s := strings.TrimSpace(md.cap); s != "" {
			mt.Cap, _ = strconv.ParseFloat(s, 64)
		}
		if md.resetKind != "never" {
			arg := strings.TrimSpace(md.resetArg)
			if arg == "" {
				arg = "1"
			}
			mt.Reset = md.resetKind + ":" + arg
		} else {
			mt.Reset = "never"
		}
		m.data.meters = append(m.data.meters, mt)
		m.gotoStep = stepMeters
	}
	return m, nil
}

// --- step: summary ---

func (m *Model) enterSummary() (tea.Model, tea.Cmd) {
	m.step = stepSummary
	d := m.data

	var b strings.Builder
	fmt.Fprintf(&b, "  %-10s %s\n", "name", d.name)
	fmt.Fprintf(&b, "  %-10s %s\n", "label", d.label)
	fmt.Fprintf(&b, "  %-10s %s\n", "kind", d.kind)
	fmt.Fprintf(&b, "  %-10s %s\n", "endpoint", d.baseURL)
	fmt.Fprintf(&b, "  %-10s %s\n", "key", secret.Redact(d.keyRef))
	var favs []string
	for _, mod := range d.models {
		if mod.Favourite {
			favs = append(favs, mod.ID)
		}
	}
	if len(favs) > 0 {
		fmt.Fprintf(&b, "  %-10s %s\n", "favourites", strings.Join(favs, ", "))
	}
	for _, mt := range d.meters {
		fmt.Fprintf(&b, "  %-10s %s (%.4g %s, resets %s)\n", "meter", mt.Name, mt.Used, mt.Unit, mt.Reset)
	}

	saveF := widgets.NewButtons(m.palette, []string{"save", "discard"})
	m.form = widgets.NewForm(m.palette, "", "", []widgets.Field{
		widgets.NewNote(m.palette, "ready to save", strings.TrimRight(b.String(), "\n")),
		saveF,
	}...)
	m.harvest = func() { d.summaryConfirm = saveF.Value() == "save" }
	m.pendingDone = func() {
		if !d.summaryConfirm {
			m.step = stepAborted
			return
		}
		if err := m.save(); err != nil {
			m.err = err
			m.step = stepAborted
			return
		}
		m.gotoStep = stepAddAnother
	}
	return m, nil
}

// save stores the key material and upserts the provider into config.
func (m *Model) save() error {
	d := m.data
	if d.keyMaterial != "" {
		if err := secret.Store(d.keyRef, d.keyMaterial); err != nil {
			return fmt.Errorf("store key: %w", err)
		}
	}
	p := BuildProvider(d.name, d.label, d.kind, d.baseURL, d.keyRef, d.note, d.models, d.meters)
	m.cfg.Upsert(p)
	return config.Save(m.cfg)
}

// --- step: add another ---

func (m *Model) enterAddAnother() (tea.Model, tea.Cmd) {
	m.step = stepAddAnother
	d := m.data
	againF := widgets.NewButtons(m.palette, []string{"add another", "done"})
	m.form = widgets.NewForm(m.palette, "", "", []widgets.Field{
		widgets.NewNote(m.palette, fmt.Sprintf("%s saved.", d.name),
			"the dashboard opens with it next time you run sslug."),
		againF,
	}...)
	m.harvest = func() { d.addAnother = againF.Value() == "add another" }
	m.pendingDone = func() {
		if d.addAnother {
			count := len(m.cfg.Providers)
			m.data = &wizardData{providerCount: count}
			m.gotoStep = stepIdentity
		} else {
			m.gotoStep = stepDone
		}
	}
	return m, nil
}

// optionalFloat validates a widgets text input that may be blank or a float.
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
