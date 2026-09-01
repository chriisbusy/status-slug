package widgets

import (
	"strings"

	"github.com/chriisbusy/status-slug/internal/theme"
)

// Form is an ordered set of fields with focus navigation, mouse support, and
// scrolling that keeps the focused field visible.
type Form struct {
	Title string
	Note  string // muted paragraph under the title

	fields  []Field
	focused int
	offset  int
	pal     theme.Palette
	done    bool
}

// NewForm builds a form from fields in display order.
func NewForm(p theme.Palette, title, note string, fields ...Field) *Form {
	f := &Form{Title: title, Note: note, fields: fields, pal: p}
	f.focused = f.firstFocusable(0)
	f.refocus()
	return f
}

func (f *Form) firstFocusable(from int) int {
	for i := from; i < len(f.fields); i++ {
		if f.fields[i].Focusable() {
			return i
		}
	}
	return -1
}

func (f *Form) lastFocusable() int {
	for i := len(f.fields) - 1; i >= 0; i-- {
		if f.fields[i].Focusable() {
			return i
		}
	}
	return -1
}

func (f *Form) refocus() {
	for i, fld := range f.fields {
		if i == f.focused {
			fld.Focus()
		} else {
			fld.Blur()
		}
	}
}

// Done reports the form was submitted past its last field.
func (f *Form) Done() bool { return f.done }

// AtFirst reports whether focus is on the first focusable field.
func (f *Form) AtFirst() bool { return f.focused == f.firstFocusable(0) }

// HandleKey routes a key: focused field first, then navigation.
// Returns submit=true when the form completes.
func (f *Form) HandleKey(key string) bool {
	if f.done || f.focused < 0 {
		return false
	}
	changed, submit := f.fields[f.focused].HandleKey(key)
	if submit {
		return f.advance()
	}
	if changed {
		return false
	}
	switch key {
	case "tab", "down":
		return f.advance()
	case "shift+tab":
		f.retreat()
	}
	return false
}

// advance moves focus forward; returns true when the form completes.
func (f *Form) advance() bool {
	next := f.firstFocusable(f.focused + 1)
	if next < 0 {
		f.done = true
		return true
	}
	f.focused = next
	f.refocus()
	return false
}

func (f *Form) retreat() {
	for i := f.focused - 1; i >= 0; i-- {
		if f.fields[i].Focusable() {
			f.focused = i
			f.refocus()
			return
		}
	}
}

// HandleClick maps a click at form-local (x, y) to a field.
func (f *Form) HandleClick(x, y, w int) {
	row := 0
	noteRows := f.headerRows(w)
	y -= noteRows
	if y < 0 {
		return
	}
	y += f.offset
	_ = row
	acc := 0
	for i, fld := range f.fields {
		fh := fld.Height(w)
		if y >= acc && y < acc+fh {
			if fld.Focusable() {
				f.focused = i
				f.refocus()
				fld.HandleClick(x, y-acc)
			}
			return
		}
		acc += fh + 1 // one blank spacer row between fields
	}
}

func (f *Form) headerRows(w int) int {
	n := 0
	if f.Title != "" {
		n++
	}
	if f.Note != "" {
		n += 2
	}
	return n
}

// Tick implements per-tick field animation (cursor blink).
func (f *Form) Tick() bool {
	redraw := false
	for _, fld := range f.fields {
		if fld.Tick() {
			redraw = true
		}
	}
	return redraw
}

// View renders the form within w×h, scrolling to keep focus visible.
func (f *Form) View(w, h int) string {
	st := newStyles(f.pal)
	var b strings.Builder
	if f.Title != "" {
		b.WriteString(st.accent.Render(f.Title) + "\n")
	}
	if f.Note != "" {
		b.WriteString(st.muted.Render(f.Note) + "\n\n")
	}

	// Lay out all fields with spacer rows; compute the focused field's band.
	type laid struct {
		text string
		rows int
	}
	var parts []laid
	focusTop, focusBot := 0, 0
	acc := 0
	for i, fld := range f.fields {
		fh := fld.Height(w)
		if i == f.focused {
			focusTop, focusBot = acc, acc+fh-1
		}
		parts = append(parts, laid{fld.View(w), fh})
		acc += fh + 1
	}

	// Adjust scroll so the focused band is visible.
	visRows := h - f.headerRows(w)
	if visRows < 3 {
		visRows = 3
	}
	if focusTop < f.offset {
		f.offset = focusTop
	}
	if focusBot >= f.offset+visRows {
		f.offset = focusBot - visRows + 1
	}

	// Emit visible rows.
	row := 0
	for i, p := range parts {
		lines := strings.Split(p.text, "\n")
		for _, l := range lines {
			if row >= f.offset && row < f.offset+visRows {
				b.WriteString(l + "\n")
			}
			row++
		}
		if i < len(parts)-1 {
			if row >= f.offset && row < f.offset+visRows {
				b.WriteString("\n")
			}
			row++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ScrollHint renders the scroll affordance line.
func (f *Form) ScrollHint(w, h int) string {
	total := 0
	for _, fld := range f.fields {
		total += fld.Height(w) + 1
	}
	visRows := h - f.headerRows(w)
	if total <= visRows {
		return ""
	}
	return scrollHint(f.offset, total, visRows)
}
