// Package wizard implements the branded setup flow as one embeddable
// bubbletea model, built on the internal widgets kit (mouse-native,
// blinking cursor, palette-exact) — no huh dependency.
package wizard

import (
	"fmt"
	"os"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/theme"
	"github.com/chriisbusy/status-slug/internal/tui/widgets"
)

// step identifies wizard stages.
type step int

const (
	stepIdentity step = iota
	stepKeySource
	stepKeyDetail
	stepValidate
	stepModels
	stepMeters
	stepMeterForm
	stepSummary
	stepAddAnother
	stepDone
	stepAborted
)

var stepNames = []string{"provider", "key", "verify", "models", "meters", "review"}

// wizardData carries answers across steps; heap-shared so model copies
// always read the same state (the value-copy trap that ate keystrokes).
type wizardData struct {
	name, label, kind, baseURL               string
	probeURL, probeMode, keyRef, keyMaterial string
	note                                     string
	validation                               check.Result
	discovered                               []string
	fetchErr                                 string
	models                                   []config.Model
	meters                                   []config.Meter
	attachCredits                            bool
	providerCount                            int

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

// Model is the wizard — embeddable as a dashboard popup or standalone.
type Model struct {
	cfg         config.Config
	palette     theme.Palette
	data        *wizardData
	step        step
	gotoStep    step
	form        *widgets.Form
	harvest     func() // syncs field values into data (called on every input)
	pendingDone func() // transition logic on form completion
	width       int
	height      int
	spin        spinner.Model
	err         error
	reconfigure string
}

// WizardInfo exposes read-only progress info for embedding hosts and tests.
type WizardInfo struct {
	Name, Label, Kind, BaseURL string
}

// Info returns the wizard's in-flight answers (read-only).
func (m Model) Info() WizardInfo {
	return WizardInfo{Name: m.data.name, Label: m.data.label, Kind: m.data.kind, BaseURL: m.data.baseURL}
}

// New builds the wizard for embedding or standalone run.
func New(cfg config.Config, reconfigure string) Model {
	palette, _ := theme.LoadFromSettings(cfg.Settings)
	m := Model{
		cfg:         cfg,
		palette:     palette,
		data:        &wizardData{providerCount: len(cfg.Providers)},
		step:        stepIdentity,
		reconfigure: reconfigure,
	}
	m.spin = spinner.New(spinner.WithSpinner(spinner.Line),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color(palette[theme.Accent]))))
	if reconfigure != "" {
		if existing := cfg.Find(reconfigure); existing != nil {
			m.data.name = existing.Name
			m.data.label = existing.Label
			m.data.kind = existing.Kind
			m.data.baseURL = existing.BaseURL
			m.data.probeURL = existing.ProbeURL
			m.data.probeMode = existing.ProbeMode
			m.data.keyRef = existing.KeyRef
			m.data.models = append([]config.Model(nil), existing.Models...)
			m.data.meters = append([]config.Meter(nil), existing.Meters...)
			for _, meter := range existing.Meters {
				if meter.Kind == "auto" && meter.Auto == "openrouter-credits" {
					m.data.attachCredits = true
					break
				}
			}
			m.data.note = existing.Note
		}
	}
	m.enterIdentity()
	m.gotoStep = stepKeySource
	return m
}

// Config returns the wizard's updated config.
func (m Model) Config() config.Config { return m.cfg }

// IsDone reports the wizard saved and finished.
func (m Model) IsDone() bool { return m.step == stepDone }

// IsAborted reports the wizard was aborted or errored out.
func (m Model) IsAborted() bool { return m.step == stepAborted || m.err != nil }

// Err returns the terminal error, if any.
func (m Model) Err() error { return m.err }

// StepName returns the current step's display name for the popup title.
func (m Model) StepName() string {
	i := int(m.step)
	switch {
	case m.step == stepKeyDetail:
		i = int(stepKeySource)
	case m.step == stepMeterForm:
		i = int(stepMeters)
	}
	if i < 0 || i >= len(stepNames) {
		return ""
	}
	return stepNames[i]
}

// blinkMsg drives the cursor blink.
type blinkMsg struct{}

func blinkTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return blinkMsg{} })
}

func (m Model) Init() tea.Cmd { return blinkTick() }

// UpdateModel is Update with a concrete return type for embedding hosts.
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	tm, cmd := m.Update(translateMouse(msg))
	if wm, ok := tm.(Model); ok {
		return wm, cmd
	}
	return m, cmd
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.rebuildCurrentForm()
		return m, nil

	case blinkMsg:
		if m.form != nil {
			m.form.Tick()
		}
		return m, blinkTick()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" || key == "esc" {
			m.step = stepAborted
			return m, tea.Quit
		}
		if m.form == nil {
			return m, nil
		}
		if m.form.HandleKey(key) {
			return m.stepCompleted()
		}
		if m.harvest != nil {
			m.harvest()
		}
		return m, nil

	case tea.MouseClickMsg:
		if m.form == nil {
			return m, nil
		}
		x, y := m.formLocal(msg.X, msg.Y)
		m.form.HandleClick(x, y, m.formWidth())
		if m.harvest != nil {
			m.harvest()
		}
		if m.form.Done() {
			return m.stepCompleted()
		}
		return m, nil

	case validateResultMsg:
		m.data.validation = msg.result
		return m.afterValidate()
	case modelsResultMsg:
		m.data.discovered = msg.ids
		m.data.fetchErr = msg.errText
		return m.enterModelsForm()
	}
	return m, nil
}

// formWidth is the content width of the wizard form.
func (m Model) formWidth() int {
	w := 72
	if m.width > 0 && m.width-10 < w {
		w = m.width - 10
	}
	if w < 36 {
		w = 36
	}
	return w
}

// formHeight is the content height of the wizard form.
func (m Model) formHeight() int {
	h := m.height - 12
	if h < 8 {
		h = 8
	}
	return h
}

// formLocal converts screen coords to form-local coords (the popup is
// centered; the dashboard passes raw screen coords through).
func (m Model) formLocal(x, y int) (int, int) {
	// The popup box: width = form width + padding 2 + border 2, centered.
	boxW := m.formWidth() + 4
	startX := (m.width - boxW) / 2
	if startX < 0 {
		startX = 0
	}
	// Estimate popup height from form height + header.
	boxH := m.formHeight() + 9
	startY := (m.height - boxH) / 2
	if startY < 0 {
		startY = 0
	}
	return x - startX - 2, y - startY - 1
}

// stepCompleted runs after a form completes: harvest, transition, dispatch.
func (m Model) stepCompleted() (tea.Model, tea.Cmd) {
	if m.harvest != nil {
		m.harvest()
	}
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
	case stepKeySource:
		return m.enterKeySource()
	case stepKeyDetail:
		return m.enterKeyDetail()
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
		return m, nil
	case stepDone:
		return m, tea.Quit
	}
	return m, nil
}

// rebuildCurrentForm reconstructs the current step's form at the new size.
// Values survive because harvest() keeps wizardData current.
func (m *Model) rebuildCurrentForm() {
	switch m.step {
	case stepIdentity:
		m.enterIdentity()
	case stepKeySource:
		m.enterKeySource()
	case stepKeyDetail:
		m.enterKeyDetail()
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
}

// translateMouse maps wheel events to arrow keys (widgets handle real clicks;
// wheel maps to up/down navigation).
func translateMouse(msg tea.Msg) tea.Msg {
	if wh, ok := msg.(tea.MouseWheelMsg); ok {
		switch wh.Button {
		case tea.MouseWheelUp:
			return tea.KeyPressMsg{Code: tea.KeyUp}
		case tea.MouseWheelDown:
			return tea.KeyPressMsg{Code: tea.KeyDown}
		}
	}
	return msg
}

// --- async result messages ---

type validateResultMsg struct{ result check.Result }
type modelsResultMsg struct {
	ids     []string
	errText string
}

// --- header & content ---

func (m Model) header() string {
	art := theme.Art(m.palette)
	stepIdx := int(m.step)
	if m.step == stepKeyDetail {
		stepIdx = int(stepKeySource)
	}
	if m.step == stepMeterForm {
		stepIdx = int(stepMeters)
	}
	if stepIdx > len(stepNames)-1 {
		stepIdx = len(stepNames) - 1
	}
	dots := ""
	for i, n := range stepNames {
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
	mode := "add another provider"
	if m.data.providerCount == 0 {
		mode = "welcome — add your first provider"
	}
	if m.reconfigure != "" {
		mode = "edit " + m.reconfigure
	}
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.KeyHint]))
	modeLine := hintStyle.Render(mode)
	hint := hintStyle.Render("tab next · shift+tab back · enter accept · esc abort · click works too")
	return art + "\n\n" + dots + "\n" + modeLine + "\n" + hint + "\n"
}

// Content renders the wizard as an embeddable string.
func (m Model) Content() string {
	return m.header() + "\n" + m.body()
}

func (m Model) body() string {
	switch m.step {
	case stepValidate:
		if m.form != nil {
			return m.form.View(m.formWidth(), m.formHeight())
		}
		return m.spin.View() + " verifying key against " + m.data.baseURL + "…"
	case stepModels:
		if m.form != nil {
			return m.form.View(m.formWidth(), m.formHeight())
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
			return m.form.View(m.formWidth(), m.formHeight())
		}
	}
	return ""
}

// View implements tea.Model for standalone runs.
func (m Model) View() tea.View {
	v := tea.NewView(m.Content())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
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
