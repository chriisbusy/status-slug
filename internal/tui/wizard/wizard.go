package wizard

// wizard.go — the setup wizard as one branded bubbletea program.
// huh forms are embedded per step; the brand header, step indicator, and
// context copy stay on screen the whole time. No stock full-screen flashes.

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/theme"
)

// step identifies wizard stages.
type step int

const (
	stepIdentity step = iota
	stepKey
	stepValidate // spinner + result
	stepModels   // spinner fetch → multiselect
	stepMeters
	stepMeterForm
	stepSummary
	stepAddAnother
	stepDone
	stepAborted
)

var stepNames = []string{"provider", "key", "verify", "models", "meters", "review"}

// wizardData carries answers across steps. Form fields bind directly to
// these so a resize can rebuild the current form without losing input.
type wizardData struct {
	name, label, kind, baseURL string
	keyRef, keyMaterial        string
	validation                 check.Result
	discovered                 []string
	fetchErr                   string
	models                     []config.Model
	meters                     []config.Meter
	attachCredits              bool
	providerCount              int // providers already in config

	// Live form field state (bound by huh fields).
	presetSel      string
	keySrc         string
	pastedKey      string
	locatePath     string
	envName        string
	fallbackChoice string
	validateChoice string
	selectedModels []string
	customModel    string
	addMeter       bool
	meterDraft     meterDraft
	summaryConfirm bool
	addAnother     bool
}

// meterDraft is the in-progress meter form.
type meterDraft struct {
	name, unit, used, cap, resetKind, resetArg string
}

// Model is the wizard tea model — embeddable as a dashboard popup or
// runnable standalone via Run.
type Model struct {
	cfg         config.Config
	palette     theme.Palette
	data        wizardData
	step        step
	gotoStep    step // set by pendingDone; stepCompleted dispatches on it
	form        *huh.Form
	width       int
	height      int
	spin        spinner.Model
	err         error
	reconfigure string
	pendingDone func()
}

// New builds the wizard for embedding (dashboard popup) or standalone run.
func New(cfg config.Config, reconfigure string) Model {
	palette, _ := theme.LoadFromSettings(cfg.Settings)
	m := Model{
		cfg:         cfg,
		palette:     palette,
		step:        stepIdentity,
		reconfigure: reconfigure,
	}
	m.data.providerCount = len(cfg.Providers)
	m.spin = spinner.New(spinner.WithSpinner(spinner.Line),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color(palette[theme.Accent]))))
	m.enterIdentity()
	m.gotoStep = stepKey
	return m
}

// StepName returns the current step's display name for the popup title.
func (m Model) StepName() string {
	i := int(m.step)
	if i < 0 || i >= len(stepNames) {
		return ""
	}
	return stepNames[i]
}

// Config returns the wizard's updated config.
func (m Model) Config() config.Config { return m.cfg }

// IsDone reports the wizard saved and finished.
func (m Model) IsDone() bool { return m.step == stepDone }

// IsAborted reports the wizard was aborted or errored out.
func (m Model) IsAborted() bool { return m.step == stepAborted || m.err != nil }

// Err returns the terminal error, if any.
func (m Model) Err() error { return m.err }

// Content renders the wizard as an embeddable string (no alt screen).
func (m Model) Content() string {
	return m.header() + "\n" + m.body()
}

// body renders the current step's interactive region.
func (m Model) body() string {
	switch m.step {
	case stepValidate:
		if m.form != nil {
			return m.form.View()
		}
		return m.spin.View() + " verifying key against " + m.data.baseURL + "…"
	case stepModels:
		if m.form != nil {
			return m.form.View()
		}
		return m.spin.View() + " discovering models…"
	case stepDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.OK])).
			Render("saved.")
	case stepAborted:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted])).
			Render("setup aborted — nothing was saved.")
	default:
		if m.form != nil {
			return m.form.View()
		}
	}
	return ""
}

// rebuildCurrentForm reconstructs the current step's huh form at the new
// terminal size. Values survive because fields bind to wizardData.
func (m *Model) rebuildCurrentForm() {
	switch m.step {
	case stepIdentity:
		m.enterIdentity()
	case stepKey:
		m.enterKey()
	case stepModels:
		if m.form != nil {
			m.enterModelsForm()
		}
	case stepMeters:
		m.enterMeters()
	case stepMeterForm:
		m.enterMeterForm()
	case stepSummary:
		m.enterSummary()
	case stepAddAnother:
		m.enterAddAnother()
	}
	// stepValidate spinner / in-flight fetches: nothing to rebuild.
}

func (m Model) Init() tea.Cmd {
	if m.form != nil {
		return m.form.Init()
	}
	return nil
}

// UpdateModel is Update with a concrete return type for embedding hosts.
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	tm, cmd := m.Update(msg)
	if wm, ok := tm.(Model); ok {
		return wm, cmd
	}
	return m, cmd
}

// Run executes the wizard standalone, returning the updated config.
func Run(cfg config.Config, reconfigure string) (config.Config, error) {
	m := New(cfg, reconfigure)
	p := tea.NewProgram(m)
	fm, err := p.Run()
	if err != nil {
		return cfg, err
	}
	final := fm.(Model)
	if final.err != nil {
		return final.cfg, final.err
	}
	return final.cfg, nil
}

// --- layout ---
func (m Model) newForm(groups ...*huh.Group) *huh.Form {
	w := 76
	if m.width > 0 && m.width-8 < w {
		w = m.width - 8
	}
	if w < 40 {
		w = 40
	}
	return huh.NewForm(groups...).
		WithTheme(theme.HuhTheme(m.palette)).
		WithWidth(w).
		WithShowHelp(true)
}

// --- layout ---

func (m Model) header() string {
	art := theme.Art(m.palette)
	if art == "" {
		art = "sslug"
	}
	// Step indicator.
	stepIdx := int(m.step)
	if stepIdx > len(stepNames)-1 {
		stepIdx = len(stepNames) - 1
	}
	dots := ""
	for i, n := range stepNames {
		if m.step == stepDone || m.step == stepAborted {
			break
		}
		mark := "○"
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
		if i < stepIdx {
			mark = "●"
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.OK]))
		} else if i == stepIdx {
			mark = "◐"
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Accent]))
		}
		dots += style.Render(mark+" "+n) + "  "
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint])).
		Render("enter next · esc abort")
	return art + "\n\n" + dots + "\n" + hint + "\n"
}

// View implements tea.Model for standalone runs.
func (m Model) View() tea.View {
	v := tea.NewView(m.Content())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// --- update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.rebuildCurrentForm()
		if m.form != nil {
			return m, m.form.Init()
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.step = stepAborted
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.step == stepValidate || (m.step == stepModels && m.form == nil) {
			return m, cmd
		}
		return m, cmd
	case validateResultMsg:
		m.data.validation = msg.result
		return m.afterValidate()
	case modelsResultMsg:
		m.data.discovered = msg.ids
		m.data.fetchErr = msg.errText
		return m.enterModelsForm()
	}
	if m.form != nil {
		f, cmd := m.form.Update(msg)
		if hf, ok := f.(*huh.Form); ok {
			m.form = hf
		}
		if m.form.State == huh.StateCompleted {
			return m.stepCompleted()
		}
		if m.form.State == huh.StateAborted {
			m.step = stepAborted
			return m, tea.Quit
		}
		return m, cmd
	}
	return m, nil
}

// stepCompleted advances the state machine after a huh form completes.
// The step's pendingDone closure (set by the enter* builder) unpacks field
// values and sets gotoStep; this dispatches to the next enter* function.
func (m Model) stepCompleted() (tea.Model, tea.Cmd) {
	if m.pendingDone != nil {
		m.pendingDone()
		m.pendingDone = nil
	}
	if m.err != nil {
		fmt.Fprintln(os.Stderr, "sslug setup:", m.err)
		return m, tea.Quit
	}
	if m.step == stepAborted {
		return m, tea.Quit
	}
	switch m.gotoStep {
	case stepKey:
		return m.enterKey()
	case stepValidate:
		return m.enterValidate()
	case stepModels:
		return m.enterModelsFetch()
	case stepMeters:
		return m.enterMeters()
	case stepMeterForm:
		return m.enterMeterForm()
	case stepSummary:
		return m.enterSummary()
	case stepAddAnother:
		return m.enterAddAnother()
	case stepIdentity:
		m.enterIdentity()
		return m, m.form.Init()
	case stepDone:
		return m, tea.Quit
	}
	return m, nil
}

// --- step: identity ---

func (m *Model) enterIdentity() {
	d := &m.data
	if m.reconfigure != "" {
		if existing := m.cfg.Find(m.reconfigure); existing != nil {
			d.name = existing.Name
			d.label = existing.Label
			d.kind = existing.Kind
			d.baseURL = existing.BaseURL
		}
	}
	if d.label == "" {
		d.label = "official"
	}

	presetOpts := make([]huh.Option[string], 0, len(provider.Presets)+1)
	for _, p := range provider.Presets {
		presetOpts = append(presetOpts, huh.NewOption(
			fmt.Sprintf("%-14s %s", p.Name, p.BaseURL), p.Name))
	}
	presetOpts = append(presetOpts, huh.NewOption("Custom — your own endpoint", "Custom"))

	presetSel := &d.presetSel
	*presetSel = "Custom"
	if d.kind != "" {
		for _, p := range provider.Presets {
			if p.Kind == d.kind && p.BaseURL == d.baseURL {
				*presetSel = p.Name
			}
		}
		if *presetSel == "Custom" && d.kind != "" && d.kind != "custom" {
			// Kind matches a preset's kind; pick the first as a hint.
			for _, p := range provider.Presets {
				if p.Kind == d.kind {
					*presetSel = p.Name
					break
				}
			}
		}
	}

	m.form = m.newForm(
		huh.NewGroup(
			huh.NewNote().
				Title("welcome to sslug").
				Description("Let's add your first provider. Pick a preset or go fully custom — sslug watches anything that speaks HTTP."),
			huh.NewInput().
				Title("What do you call it?").
				Placeholder("e.g. OpenAI, or Neuralwatt (homelab)").
				Value(&d.name).
				Validate(func(s string) error {
					s = strings.TrimSpace(s)
					if s == "" {
						return fmt.Errorf("a name is required")
					}
					if m.reconfigure == "" && m.cfg.Find(s) != nil {
						return fmt.Errorf("%q already exists", s)
					}
					return nil
				}),
			huh.NewSelect[string]().
				Title("How is it billed / provided?").
				Options(
					huh.NewOption("official — first-party API", "official"),
					huh.NewOption("third-party — reseller or gateway", "third-party"),
					huh.NewOption("coding-plan — subscription bundle", "coding-plan"),
					huh.NewOption("custom — something else entirely", "custom"),
				).
				Value(&d.label),
			huh.NewSelect[string]().
				Title("Which preset?").
				Description("Presets fill in the endpoint and auth style. Custom works with any OpenAI-compatible API.").
				Options(presetOpts...).
				Value(presetSel),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Base URL").
				Description("The API root, e.g. https://api.example.com/v1").
				Placeholder("https://…").
				Value(&d.baseURL).
				Validate(func(s string) error {
					if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
						return fmt.Errorf("must start with http:// or https://")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return *presetSel != "Custom" }),
	)
	d.name = strings.TrimSpace(d.name)
	m.onFormDone(func() {
		if *presetSel == "Custom" {
			d.kind = "custom"
		} else if p := provider.FindPreset(*presetSel); p != nil {
			d.kind = p.Kind
			d.baseURL = p.BaseURL
		}
		m.gotoStep = stepKey
	})
}

// onFormDone registers a callback fired when the current form completes.
// Stored on the model so stepCompleted can invoke it.
func (m *Model) onFormDone(fn func()) { m.pendingDone = fn }

// --- key step is in wizard_key.go (next file) ---

// validateResultMsg / modelsResultMsg carry async results.
type validateResultMsg struct{ result check.Result }
type modelsResultMsg struct {
	ids     []string
	errText string
}
