package theme

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// LerpHex interpolates two #RRGGBB colors; t in [0,1].
// Invalid input returns the other endpoint (t clamped).
func LerpHex(a, b string, t float64) string {
	ar, ag, ab, okA := parseHex(a)
	br, bg, bb, okB := parseHex(b)
	if !okA || !okB {
		if t < 0.5 {
			return a
		}
		return b
	}
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return fmt.Sprintf("#%02X%02X%02X",
		ar+int(float64(br-ar)*t),
		ag+int(float64(bg-ag)*t),
		ab+int(float64(bb-ab)*t),
	)
}

func parseHex(s string) (r, g, b int, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xFF), int(v >> 8 & 0xFF), int(v & 0xFF), true
}

// GradientText paints each rune of text with a color swept lo→hi left to
// right, per line. Empty palette colors (mono) render text unstyled.
func GradientText(text, lo, hi string) string {
	if lo == "" || hi == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for li, line := range lines {
		runes := []rune(line)
		n := len(runes)
		if n == 0 {
			continue
		}
		var b strings.Builder
		for i, r := range runes {
			t := float64(i) / float64(n-1)
			if n == 1 {
				t = 0
			}
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(LerpHex(lo, hi, t))).Render(string(r)))
		}
		lines[li] = b.String()
	}
	return strings.Join(lines, "\n")
}

// ArtLines are the sslug mark — SSLUG in a two-row block font,
// drawn with a gradient sweep.
var ArtLines = []string{
	"█▀▀ █▀▀ █   █ █ ▄▀▀",
	"▄▄█ ▄▄█ █▄▄ █▄█ █▄█",
}

// Art renders the SSLUG block art with per-letter colors from the palette —
// btop-logo style, each letter its own role — plus the muted wordmark.
func Art(p Palette) string {
	roles := []Role{Accent, GradHi, OK, Warn, Err}
	// Letter column spans in the fixed two-row font.
	spans := [][2]int{{0, 3}, {4, 7}, {8, 11}, {12, 15}, {16, 19}}
	var lines []string
	for _, row := range ArtLines {
		runes := []rune(row)
		var b strings.Builder
		for i, span := range spans {
			chunk := string(runes[span[0]:span[1]])
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(p[roles[i]])).Render(chunk))
			if span[1] < len(runes) {
				b.WriteString(" ")
			}
		}
		lines = append(lines, b.String())
	}
	if len(lines) > 1 {
		lines[1] += lipgloss.NewStyle().Foreground(lipgloss.Color(p[Muted])).Render("  · status slug")
	}
	return strings.Join(lines, "\n")
}
