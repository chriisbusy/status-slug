package wizard

// steps.go — per-step huh form builders and transitions for the wizard.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/theme"
)

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

// statusColor maps a check status to a palette color.
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

// --- step: key ---

func (m *Model) enterKey() (tea.Model, tea.Cmd) {
	d := m.data
	m.step = stepKey

	srcOpts := []huh.Option[string]{
		huh.NewOption("Paste a key — stored in your OS keyring", "paste"),
		huh.NewOption("Read it from a file…", "locate"),
	}
	for _, v := range DetectEnvVars(os.Environ()) {
		srcOpts = append(srcOpts, huh.NewOption("Use $"+v+" (detected in your environment)", "env:"+v))
	}
	srcOpts = append(srcOpts, huh.NewOption("No key — local or unauthenticated", "none"))

	groups := []*huh.Group{
		huh.NewGroup(
			huh.NewNote().
				Title("the key stays yours").
				Description("Keys go to your OS keyring and are never printed, logged, or sent anywhere except this provider's API. Read-only probes only."),
			huh.NewSelect[string]().
				Title("Where's the key?").
				Options(srcOpts...).
				Value(&d.keySrc),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Paste it in").
				Description("Stored in the OS keyring (service: sslug).").
				Password(true).
				Value(&d.pastedKey).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("empty key")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return d.keySrc != "paste" }),
		huh.NewGroup(
			huh.NewFilePicker().
				Title("Point me at the key file").
				Description("Its contents become the key; the file itself is left alone.").
				Value(&d.locatePath),
		).WithHideFunc(func() bool { return d.keySrc != "locate" }),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("No OS keyring here (headless?). Store it how?").
				Options(
					huh.NewOption("0600 file — plaintext at rest, like curl .netrc", "file"),
					huh.NewOption("Keep it in an env var instead", "env"),
					huh.NewOption("Abort", "abort"),
				).
				Value(&d.fallbackChoice),
		).WithHideFunc(func() bool {
			// Only when we have material AND keyring is unusable AND keys_source allows choosing.
			if d.keySrc != "paste" && d.keySrc != "locate" {
				return true
			}
			ks := m.cfg.Settings.KeysSource
			if ks == "keyring" || ks == "file" || ks == "env" {
				return true // destination forced by settings; no dialog
			}
			return secret.KeyringAvailable()
		}),
		huh.NewGroup(
			huh.NewInput().
				Title("Which environment variable holds it?").
				Placeholder("MY_PROVIDER_API_KEY").
				Value(&d.envName),
		).WithHideFunc(func() bool { return d.fallbackChoice != "env" }),
	}

	m.form = m.newForm(groups...)
	m.onFormDone(func() {
		switch {
		case d.keySrc == "none":
			d.keyRef, d.keyMaterial, _ = KeyRef("none", "", d.name)
		case d.keySrc == "paste", d.keySrc == "locate":
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
			dest := m.cfg.Settings.KeysSource
			switch dest {
			case "keyring", "file":
				// forced by settings
			case "env":
				d.keyRef, _, m.err = KeyRef("env", d.envName, d.name)
				return
			default: // auto
				if secret.KeyringAvailable() {
					dest = "keyring"
				} else if d.fallbackChoice == "abort" {
					m.step = stepAborted
					return
				} else if d.fallbackChoice == "env" {
					d.keyRef, _, m.err = KeyRef("env", d.envName, d.name)
					return
				} else {
					dest = "file"
				}
			}
			d.keyRef, d.keyMaterial, m.err = KeyRef(dest, material, d.name)
		case strings.HasPrefix(d.keySrc, "env:"):
			v := strings.TrimPrefix(d.keySrc, "env:")
			d.keyRef, _, m.err = KeyRef("env", v, d.name)
		}
		if m.step != stepAborted {
			m.gotoStep = stepValidate
		}
	})
	return m, m.form.Init()
}

// --- step: validate (async probe with spinner) ---

func (m *Model) enterValidate() (tea.Model, tea.Cmd) {
	if m.err != nil {
		return m, tea.Quit
	}
	m.step = stepValidate
	d := m.data
	if d.keyRef == "" || d.keyRef == "none" || d.baseURL == "" {
		// Nothing to validate against.
		return m.enterModelsFetch()
	}
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
	// Not ok: offer choices.
	status := lipgloss.NewStyle().Foreground(lipgloss.Color(statusColor(m.palette, string(d.validation.Status)))).
		Render(string(d.validation.Status))
	opts := []huh.Option[string]{
		huh.NewOption("Save anyway — I'll fix it later", "save"),
		huh.NewOption("Re-enter the key", "retry"),
		huh.NewOption("Abort setup", "abort"),
	}
	m.form = m.newForm(huh.NewGroup(
		huh.NewNote().
			Title("probe came back "+status).
			Description(d.validation.Reason),
		huh.NewSelect[string]().
			Title("What now?").
			Options(opts...).
			Value(&d.validateChoice),
	))
	m.step = stepValidate
	m.onFormDone(func() {
		switch d.validateChoice {
		case "retry":
			d.keyRef, d.keyMaterial = "", ""
			m.gotoStep = stepKey
		case "abort":
			m.step = stepAborted
		default:
			m.gotoStep = stepModels
		}
	})
	return m, m.form.Init()
}

// --- step: models (async fetch → multiselect) ---

func (m *Model) enterModelsFetch() (tea.Model, tea.Cmd) {
	m.step = stepModels
	d := m.data
	if d.baseURL == "" {
		// No endpoint — skip fetch, manual entry only.
		return m.enterModelsForm()
	}
	m.form = nil // spinner view while fetching
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

	var note *huh.Note
	if d.fetchErr != "" {
		note = huh.NewNote().
			Title("couldn't fetch the model list").
			Description("Add model IDs by hand below — or finish now and the dashboard can fetch them later.")
	} else if len(d.discovered) == 0 {
		note = huh.NewNote().
			Title("no models discovered").
			Description("Add IDs by hand below, or finish and pick them later.")
	} else {
		note = huh.NewNote().
			Title(fmt.Sprintf("%d models found", len(d.discovered))).
			Description("Space to mark favourites — they get latency probes and their own cockpit pane.")
	}

	groups := []*huh.Group{}
	if len(d.discovered) > 0 {
		opts := make([]huh.Option[string], len(d.discovered))
		for i, id := range d.discovered {
			opts[i] = huh.NewOption(id, id)
		}
		groups = append(groups, huh.NewGroup(
			note,
			huh.NewMultiSelect[string]().
				Title("Favourites").
				Options(opts...).
				Value(&d.selectedModels),
		))
	} else {
		groups = append(groups, huh.NewGroup(note))
	}
	groups = append(groups, huh.NewGroup(
		huh.NewInput().
			Title("Any other model ID? (blank to skip)").
			Placeholder("e.g. neuralwatt-large-v3").
			Value(&d.customModel),
	))

	m.form = m.newForm(groups...)
	m.onFormDone(func() {
		customList := []string{}
		if strings.TrimSpace(d.customModel) != "" {
			customList = []string{strings.TrimSpace(d.customModel)}
		}
		// Mark selected as favourites.
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
	})
	return m, m.form.Init()
}

// --- step: meters ---

func (m *Model) enterMeters() (tea.Model, tea.Cmd) {
	m.step = stepMeters
	d := m.data
	d.addMeter = false

	isOpenRouter := strings.Contains(m.data.baseURL, "openrouter.ai")

	groups := []*huh.Group{
		huh.NewGroup(
			huh.NewNote().
				Title("usage meters — track anything").
				Description("Energy, spend, requests, credits — any unit, with a cap and a reset cycle. Update them from scripts with `sslug usage set`. Skip freely: the dashboard shows probes either way."),
			huh.NewConfirm().
				Title("Add a usage meter?").
				Value(&d.addMeter),
		),
	}
	if isOpenRouter {
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().
				Title("OpenRouter detected — attach the auto credits meter?").
				Description("Fetched live from the OpenRouter credits API on every check.").
				Value(&d.attachCredits),
		))
	}

	m.form = m.newForm(groups...)
	m.onFormDone(func() {
		if d.attachCredits {
			m.data.meters = append(m.data.meters, config.Meter{
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
	})
	return m, m.form.Init()
}

// enterMeterForm runs one meter definition; loops while the user keeps adding.
func (m *Model) enterMeterForm() (tea.Model, tea.Cmd) {
	md := m.data.meterDraft

	m.form = m.newForm(huh.NewGroup(
		huh.NewInput().Title("Meter name").Placeholder("Energy, Spend, Requests…").
			Value(&md.name).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("name required")
			}
			return nil
		}),
		huh.NewInput().Title("Unit").Placeholder("USD").Value(&md.unit),
		huh.NewInput().Title("Current value (blank = 0)").Value(&md.used).Validate(optionalFloat),
		huh.NewInput().Title("Cap (blank = no cap)").Value(&md.cap).Validate(optionalFloat),
		huh.NewSelect[string]().Title("Resets").
			Options(
				huh.NewOption("never", "never"),
				huh.NewOption("monthly — on a day of the month", "monthly"),
				huh.NewOption("weekly — on a weekday", "weekly"),
				huh.NewOption("on a fixed date", "date"),
			).Value(&md.resetKind),
		huh.NewInput().Title("Reset argument").
			Description("monthly: day 1–31 · weekly: mon..sun · date: YYYY-MM-DD").
			Value(&md.resetArg).
			Validate(resetArgValidator(&md.resetKind)),
	))
	m.onFormDone(func() {
		mt := config.Meter{Name: strings.TrimSpace(md.name), Unit: strings.TrimSpace(md.unit), Kind: "manual"}
		if s := strings.TrimSpace(md.used); s != "" {
			mt.Used, _ = strconv.ParseFloat(s, 64)
		}
		if s := strings.TrimSpace(md.cap); s != "" {
			mt.Cap, _ = strconv.ParseFloat(s, 64)
		}
		if md.resetKind != "never" {
			mt.Reset = md.resetKind + ":" + strings.TrimSpace(md.resetArg)
		} else {
			mt.Reset = "never"
		}
		m.data.meters = append(m.data.meters, mt)
		m.gotoStep = stepMeters
	})
	return m, m.form.Init()
}

func resetArgValidator(kind *string) func(string) error {
	return func(s string) error {
		s = strings.TrimSpace(s)
		switch *kind {
		case "never":
			return nil
		case "monthly":
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 31 {
				return fmt.Errorf("day of month, 1–31")
			}
		case "weekly":
			switch strings.ToLower(s) {
			case "mon", "tue", "wed", "thu", "fri", "sat", "sun":
			default:
				return fmt.Errorf("mon, tue, wed, thu, fri, sat or sun")
			}
		case "date":
			if _, err := time.Parse("2006-01-02", s); err != nil {
				return fmt.Errorf("YYYY-MM-DD")
			}
		}
		return nil
	}
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
	if len(d.models) > 0 {
		var favs []string
		for _, mod := range d.models {
			if mod.Favourite {
				favs = append(favs, mod.ID)
			}
		}
		fmt.Fprintf(&b, "  %-10s %s\n", "favourites", strings.Join(favs, ", "))
	}
	for _, mt := range d.meters {
		fmt.Fprintf(&b, "  %-10s %s (%.4g %s, resets %s)\n", "meter", mt.Name, mt.Used, mt.Unit, mt.Reset)
	}

	m.form = m.newForm(huh.NewGroup(
		huh.NewNote().
			Title("ready to save").
			Description(b.String()),
		huh.NewConfirm().
			Title("Save this provider?").
			Affirmative("Save").
			Negative("Discard").
			Value(&d.summaryConfirm),
	))
	m.onFormDone(func() {
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
	})
	return m, m.form.Init()
}

// save stores the key material and upserts the provider into config.
func (m *Model) save() error {
	d := m.data
	if d.keyMaterial != "" {
		if err := secret.Store(d.keyRef, d.keyMaterial); err != nil {
			return fmt.Errorf("store key: %w", err)
		}
	}
	p := BuildProvider(d.name, d.label, d.kind, d.baseURL, d.keyRef, "", d.models, d.meters)
	m.cfg.Upsert(p)
	return config.Save(m.cfg)
}

// --- step: add another ---

func (m *Model) enterAddAnother() (tea.Model, tea.Cmd) {
	m.step = stepAddAnother
	d := m.data
	m.form = m.newForm(huh.NewGroup(
		huh.NewNote().
			Title(fmt.Sprintf("%s saved.", m.data.name)).
			Description("The dashboard opens with it next time you run sslug."),
		huh.NewConfirm().
			Title("Add another provider?").
			Value(&d.addAnother),
	))
	m.onFormDone(func() {
		if d.addAnother {
			m.data = &wizardData{providerCount: len(m.cfg.Providers)}
			m.gotoStep = stepIdentity
		} else {
			m.gotoStep = stepDone
		}
	})
	return m, m.form.Init()
}
