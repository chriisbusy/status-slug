package widgets

import (
	"strings"

	"github.com/chriisbusy/status-slug/internal/theme"
)

// Note is a non-focusable display-only field (section text, hints, cards).
type Note struct {
	Title string
	Body  string

	st styles
}

// NewNote builds a Note field.
func NewNote(p theme.Palette, title, body string) *Note {
	return &Note{Title: title, Body: body, st: newStyles(p)}
}

// SetBody updates the note text.
func (n *Note) SetBody(body string) { n.Body = body }

func (n *Note) Focusable() bool { return false }
func (n *Note) Focus()          {}
func (n *Note) Blur()           {}
func (n *Note) Focused() bool   { return false }
func (n *Note) Tick() bool      { return false }
func (n *Note) HandleKey(k string) (bool, bool) {
	return false, false
}
func (n *Note) HandleClick(x, y int) bool { return false }

func (n *Note) Height(w int) int {
	h := 0
	if n.Title != "" {
		h++
	}
	if n.Body != "" {
		h += len(wrapText(n.Body, w))
	}
	return h
}

func (n *Note) View(w int) string {
	out := ""
	if n.Title != "" {
		out += n.st.accent.Render(n.Title) + "\n"
	}
	if n.Body != "" {
		for _, l := range wrapText(n.Body, w) {
			out += n.st.muted.Render(l) + "\n"
		}
	}
	if out == "" {
		return ""
	}
	return out[:len(out)-1]
}

// wrapText wraps s to w cells on word boundaries.
func wrapText(s string, w int) []string {
	if w < 10 {
		w = 10
	}
	var lines []string
	words := strings.Fields(s)
	cur := ""
	for _, word := range words {
		if cur == "" {
			cur = word
			continue
		}
		if len(cur)+1+len(word) > w {
			lines = append(lines, cur)
			cur = word
		} else {
			cur += " " + word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}
