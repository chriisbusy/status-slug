// Package widgets provides the form fields used by the setup wizard and the
// dashboard's settings/meter forms: text, select, multi-select, confirm,
// and buttons — all with mouse support, blinking cursors, and palette-native
// styling. Built because huh v2 has no mouse support and no control over
// cursor blink; these widgets exist to be fully interactive.
package widgets

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/chriisbusy/status-slug/internal/theme"
)

// Field is one interactive form element. Coordinates in HandleClick are
// relative to the field's own top-left cell.
type Field interface {
	// View renders the field within w cells.
	View(w int) string
	// Height returns the rendered height in rows at width w.
	Height(w int) int
	// Focusable reports whether the field can hold focus.
	Focusable() bool
	Focus()
	Blur()
	Focused() bool
	// HandleKey processes a key; submit is true when the key means
	// "accept this field and move on" (enter).
	HandleKey(key string) (changed, submit bool)
	// HandleClick processes a click at local (x, y); handled is true when
	// the click did something (focus, toggle, select).
	HandleClick(x, y int) (handled bool)
	// Tick advances blink/animation state; returns true if a redraw matters.
	Tick() bool
}

// blinkRate is the cursor blink half-period in tea ticks.
const blinkRate = 5

// --- shared style helpers ---

type styles struct {
	label, value, placeholder, muted, accent, ok, err, selBg, selFg lipgloss.Style
}

func newStyles(p theme.Palette) styles {
	fg := func(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }
	return styles{
		label:       fg(p[theme.Title]).Bold(true),
		value:       fg(p[theme.Fg]),
		placeholder: fg(p[theme.Muted]),
		muted:       fg(p[theme.Muted]),
		accent:      fg(p[theme.Accent]).Bold(true),
		ok:          fg(p[theme.OK]),
		err:         fg(p[theme.Err]),
		selBg:       fg(p[theme.SelectedFg]).Background(lipgloss.Color(p[theme.SelectedBg])),
		selFg:       fg(p[theme.SelectedFg]),
	}
}

// OptionalFloat validates a text input that may be blank or a float.
func OptionalFloat(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return fmt.Errorf("must be a number")
	}
	return nil
}

// --- text field ---

// TextField is a single-line input with placeholder, optional password
// masking, validation, and a blinking block cursor.
type TextField struct {
	Label       string
	Value       string
	Placeholder string
	Password    bool
	Validate    func(string) error

	st      styles
	focused bool
	cursor  int // rune offset into Value
	blink   int
	err     error
}

// NewText builds a TextField.
func NewText(p theme.Palette, label, placeholder string) *TextField {
	return &TextField{Label: label, Placeholder: placeholder, st: newStyles(p)}
}

// Err returns the current validation error.
func (t *TextField) Err() error { return t.err }

func (t *TextField) Focusable() bool { return true }
func (t *TextField) Focus()          { t.focused = true; t.blink = 0 }
func (t *TextField) Blur()           { t.focused = false }
func (t *TextField) Focused() bool   { return t.focused }

// Tick implements Field: toggles cursor visibility.
func (t *TextField) Tick() bool {
	if !t.focused {
		return false
	}
	t.blink++
	return true
}

func (t *TextField) Height(w int) int {
	h := 2 // label line + input line
	if t.err != nil {
		h++
	}
	return h
}

// display returns the (possibly masked) value and cursor rune offset.
func (t *TextField) display() (string, int) {
	v := t.Value
	c := t.cursor
	if t.Password {
		v = strings.Repeat("•", len([]rune(v)))
	}
	return v, c
}

func (t *TextField) View(w int) string {
	label := t.st.label.Render(t.Label)
	if t.focused {
		label = t.st.accent.Render(t.Label)
	}
	v, ci := t.display()
	runes := []rune(v)
	if ci > len(runes) {
		ci = len(runes)
	}
	// Clamp cursor into the visible window.
	if w < 8 {
		w = 8
	}
	vis := w - 2
	start := 0
	if ci >= vis {
		start = ci - vis + 1
	}
	var b strings.Builder
	b.WriteString(" ")
	shown := false
	for i := start; i < start+vis; i++ {
		idx := i - start
		_ = idx
		if t.focused && i == ci && t.blink%(2*blinkRate) < blinkRate {
			// Block cursor: accent cell, with the rune under it if any.
			under := " "
			if i < len(runes) {
				under = string(runes[i])
			}
			b.WriteString(t.st.selBg.Render(under))
			shown = true
			continue
		}
		if i < len(runes) {
			b.WriteString(t.st.value.Render(string(runes[i])))
		} else if i == start && v == "" {
			b.WriteString(t.st.placeholder.Render(t.Placeholder))
			break
		} else {
			b.WriteString(" ")
		}
	}
	if t.focused && ci == len(runes) && !shown && t.blink%(2*blinkRate) < blinkRate {
		b.WriteString(t.st.selBg.Render(" "))
	}
	line := b.String()
	if t.err != nil {
		line += "\n" + t.st.err.Render("  "+t.err.Error())
	}
	return label + "\n" + line
}

// HandleKey implements Field.
func (t *TextField) HandleKey(key string) (changed, submit bool) {
	switch key {
	case "enter":
		if t.Validate != nil {
			t.err = t.Validate(t.Value)
		}
		return t.err == nil, t.err == nil
	case "backspace":
		r := []rune(t.Value)
		if t.cursor > 0 && len(r) > 0 {
			t.Value = string(append(r[:t.cursor-1], r[t.cursor:]...))
			t.cursor--
			return true, false
		}
	case "left":
		if t.cursor > 0 {
			t.cursor--
			return true, false
		}
	case "right":
		if t.cursor < len([]rune(t.Value)) {
			t.cursor++
			return true, false
		}
	case "home":
		t.cursor = 0
		return true, false
	case "end":
		t.cursor = len([]rune(t.Value))
		return true, false
	case "ctrl+u":
		t.Value = ""
		t.cursor = 0
		return true, false
	default:
		if len([]rune(key)) == 1 && key >= " " {
			r := []rune(t.Value)
			r = append(r[:t.cursor], append([]rune(key), r[t.cursor:]...)...)
			t.Value = string(r)
			t.cursor++
			return true, false
		}
	}
	return false, false
}

// HandleClick implements Field: click positions the cursor.
func (t *TextField) HandleClick(x, y int) bool {
	if y == 0 {
		return false // label row: just focus (handled by the form)
	}
	v, _ := t.display()
	vis := x - 1
	if vis < 0 {
		vis = 0
	}
	if vis > len([]rune(v)) {
		vis = len([]rune(v))
	}
	t.cursor = vis
	return true
}

// --- select field ---

// SelectField is a vertical single-choice list with wheel scroll and click
// selection.
type SelectField struct {
	Label    string
	Options  []string
	Selected int
	Hint     string // muted line under the label

	st      styles
	focused bool
	offset  int
}

// NewSelect builds a SelectField.
func NewSelect(p theme.Palette, label string, options []string) *SelectField {
	return &SelectField{Label: label, Options: options, st: newStyles(p)}
}

func (s *SelectField) Focusable() bool { return len(s.Options) > 0 }
func (s *SelectField) Focus()          { s.focused = true }
func (s *SelectField) Blur()           { s.focused = false }
func (s *SelectField) Focused() bool   { return s.focused }
func (s *SelectField) Tick() bool      { return false }

// Value returns the selected option text.
func (s *SelectField) Value() string {
	if s.Selected >= 0 && s.Selected < len(s.Options) {
		return s.Options[s.Selected]
	}
	return ""
}

// visibleRows caps how many options render at once.
const visibleRows = 8

func (s *SelectField) Height(w int) int {
	n := len(s.Options)
	if n > visibleRows {
		n = visibleRows
	}
	h := 1 + n
	if s.Hint != "" {
		h++
	}
	if len(s.Options) > visibleRows {
		h++ // scroll indicator line
	}
	return h
}

func (s *SelectField) View(w int) string {
	label := s.st.label.Render(s.Label)
	if s.focused {
		label = s.st.accent.Render(s.Label)
	}
	var b strings.Builder
	b.WriteString(label)
	if s.Hint != "" {
		b.WriteString("\n" + s.st.muted.Render(s.Hint))
	}
	b.WriteString("\n")
	// Window the options around the selection.
	if s.Selected < s.offset {
		s.offset = s.Selected
	}
	if s.Selected >= s.offset+visibleRows {
		s.offset = s.Selected - visibleRows + 1
	}
	end := s.offset + visibleRows
	if end > len(s.Options) {
		end = len(s.Options)
	}
	for i := s.offset; i < end; i++ {
		opt := s.Options[i]
		if i == s.Selected {
			marker := "◆ "
			if !s.focused {
				marker = "◇ "
			}
			if s.focused {
				b.WriteString(s.st.selBg.Render("> "+opt) + "\n")
			} else {
				b.WriteString(s.st.ok.Render(marker) + s.st.value.Render(opt) + "\n")
			}
		} else {
			b.WriteString(s.st.value.Render("  "+opt) + "\n")
		}
	}
	if len(s.Options) > visibleRows {
		b.WriteString(s.st.muted.Render(scrollHint(s.offset, len(s.Options), visibleRows)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func scrollHint(offset, total, vis int) string {
	more := ""
	if offset > 0 {
		more += "↑ "
	}
	if offset+vis < total {
		more += "↓ "
	}
	return "  " + more + "scroll"
}

// HandleKey implements Field.
func (s *SelectField) HandleKey(key string) (changed, submit bool) {
	switch key {
	case "up", "k":
		if s.Selected > 0 {
			s.Selected--
			return true, false
		}
	case "down", "j":
		if s.Selected < len(s.Options)-1 {
			s.Selected++
			return true, false
		}
	case "home":
		s.Selected = 0
		return true, false
	case "end":
		s.Selected = len(s.Options) - 1
		return true, false
	case "enter":
		return true, true
	}
	return false, false
}

// HandleClick implements Field: click an option to select+submit.
func (s *SelectField) HandleClick(x, y int) bool {
	headerRows := 1
	if s.Hint != "" {
		headerRows++
	}
	row := y - headerRows
	if row < 0 || row >= visibleRows {
		return false
	}
	idx := s.offset + row
	if idx >= 0 && idx < len(s.Options) {
		s.Selected = idx
		return true
	}
	return false
}

// --- multi-select field ---

// MultiField is a checkbox list: space/click toggles, enter accepts.
type MultiField struct {
	Label   string
	Options []string
	Checked []bool
	Hint    string

	st      styles
	focused bool
	cursor  int
	offset  int
}

// NewMulti builds a MultiField.
func NewMulti(p theme.Palette, label string, options []string) *MultiField {
	return &MultiField{Label: label, Options: options, Checked: make([]bool, len(options)), st: newStyles(p)}
}

func (m *MultiField) Focusable() bool { return len(m.Options) > 0 }
func (m *MultiField) Focus()          { m.focused = true }
func (m *MultiField) Blur()           { m.focused = false }
func (m *MultiField) Focused() bool   { return m.focused }
func (m *MultiField) Tick() bool      { return false }

// Values returns the checked options.
func (m *MultiField) Values() []string {
	var out []string
	for i, o := range m.Options {
		if i < len(m.Checked) && m.Checked[i] {
			out = append(out, o)
		}
	}
	return out
}

func (m *MultiField) Height(w int) int {
	n := len(m.Options)
	if n > visibleRows {
		n = visibleRows
	}
	h := 1 + n
	if m.Hint != "" {
		h++
	}
	if len(m.Options) > visibleRows {
		h++
	}
	return h
}

func (m *MultiField) View(w int) string {
	label := m.st.label.Render(m.Label)
	if m.focused {
		label = m.st.accent.Render(m.Label)
	}
	var b strings.Builder
	b.WriteString(label)
	if m.Hint != "" {
		b.WriteString("\n" + m.st.muted.Render(m.Hint))
	}
	b.WriteString("\n")
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visibleRows {
		m.offset = m.cursor - visibleRows + 1
	}
	end := m.offset + visibleRows
	if end > len(m.Options) {
		end = len(m.Options)
	}
	for i := m.offset; i < end; i++ {
		box := "☐ "
		if i < len(m.Checked) && m.Checked[i] {
			box = "☑ "
		}
		line := box + m.Options[i]
		if i == m.cursor && m.focused {
			b.WriteString(m.st.selBg.Render("> "+line) + "\n")
		} else if i < len(m.Checked) && m.Checked[i] {
			b.WriteString("  " + m.st.ok.Render(box) + m.st.value.Render(m.Options[i]) + "\n")
		} else {
			b.WriteString(m.st.value.Render("  "+line) + "\n")
		}
	}
	if len(m.Options) > visibleRows {
		b.WriteString(m.st.muted.Render(scrollHint(m.offset, len(m.Options), visibleRows)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// HandleKey implements Field.
func (m *MultiField) HandleKey(key string) (changed, submit bool) {
	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			return true, false
		}
	case "down", "j":
		if m.cursor < len(m.Options)-1 {
			m.cursor++
			return true, false
		}
	case " ":
		if m.cursor < len(m.Checked) {
			m.Checked[m.cursor] = !m.Checked[m.cursor]
			return true, false
		}
	case "enter":
		return true, true
	}
	return false, false
}

// HandleClick implements Field: click toggles the row.
func (m *MultiField) HandleClick(x, y int) bool {
	headerRows := 1
	if m.Hint != "" {
		headerRows++
	}
	row := y - headerRows
	if row < 0 || row >= visibleRows {
		return false
	}
	idx := m.offset + row
	if idx >= 0 && idx < len(m.Options) {
		m.cursor = idx
		if idx < len(m.Checked) {
			m.Checked[idx] = !m.Checked[idx]
		}
		return true
	}
	return false
}

// --- confirm field ---

// ConfirmField is a yes/no toggle.
type ConfirmField struct {
	Label string
	Value bool
	Hint  string

	st      styles
	focused bool
}

// NewConfirm builds a ConfirmField.
func NewConfirm(p theme.Palette, label string, value bool) *ConfirmField {
	return &ConfirmField{Label: label, Value: value, st: newStyles(p)}
}

func (c *ConfirmField) Focusable() bool { return true }
func (c *ConfirmField) Focus()          { c.focused = true }
func (c *ConfirmField) Blur()           { c.focused = false }
func (c *ConfirmField) Focused() bool   { return c.focused }
func (c *ConfirmField) Tick() bool      { return false }
func (c *ConfirmField) Height(w int) int {
	if c.Hint != "" {
		return 2
	}
	return 1
}

func (c *ConfirmField) View(w int) string {
	mark := "○ no "
	if c.Value {
		mark = "● yes"
	}
	style := c.st.value
	if c.focused {
		style = c.st.selBg
	}
	line := style.Render(mark + "  " + c.Label)
	if c.Hint != "" {
		line += "\n" + c.st.muted.Render(c.Hint)
	}
	return line
}

// HandleKey implements Field.
func (c *ConfirmField) HandleKey(key string) (changed, submit bool) {
	switch key {
	case " ", "enter":
		c.Value = !c.Value
		return true, key == "enter"
	}
	return false, false
}

// HandleClick implements Field.
func (c *ConfirmField) HandleClick(x, y int) bool {
	if y == 0 {
		c.Value = !c.Value
		return true
	}
	return false
}

// --- button row ---

// ButtonRow is a horizontal row of action buttons.
type ButtonRow struct {
	Buttons  []string
	Selected int

	st      styles
	focused bool
	xpos    []int // computed at render for click mapping
}

// NewButtons builds a ButtonRow.
func NewButtons(p theme.Palette, buttons []string) *ButtonRow {
	return &ButtonRow{Buttons: buttons, st: newStyles(p)}
}

func (b *ButtonRow) Focusable() bool  { return len(b.Buttons) > 0 }
func (b *ButtonRow) Focus()           { b.focused = true }
func (b *ButtonRow) Blur()            { b.focused = false }
func (b *ButtonRow) Focused() bool    { return b.focused }
func (b *ButtonRow) Tick() bool       { return false }
func (b *ButtonRow) Height(w int) int { return 1 }

// Value returns the selected button label.
func (b *ButtonRow) Value() string {
	if b.Selected >= 0 && b.Selected < len(b.Buttons) {
		return b.Buttons[b.Selected]
	}
	return ""
}

func (b *ButtonRow) View(w int) string {
	b.xpos = b.xpos[:0]
	var out strings.Builder
	x := 0
	for i, label := range b.Buttons {
		text := " " + label + " "
		style := b.st.muted
		if b.focused && i == b.Selected {
			style = b.st.selBg.Bold(true)
		}
		b.xpos = append(b.xpos, x)
		out.WriteString(style.Render(text))
		out.WriteString("  ")
		x += len([]rune(text)) + 2
	}
	return out.String()
}

// HandleKey implements Field.
func (b *ButtonRow) HandleKey(key string) (changed, submit bool) {
	switch key {
	case "left", "h":
		if b.Selected > 0 {
			b.Selected--
			return true, false
		}
	case "right", "l":
		if b.Selected < len(b.Buttons)-1 {
			b.Selected++
			return true, false
		}
	case "enter", " ":
		return true, true
	}
	return false, false
}

// HandleClick implements Field: click a button to select+submit it.
func (b *ButtonRow) HandleClick(x, y int) bool {
	for i := range b.Buttons {
		w := len([]rune(b.Buttons[i])) + 2
		if x >= b.xpos[i] && x < b.xpos[i]+w {
			b.Selected = i
			return true
		}
	}
	return false
}
