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

// Art renders the brand art with the palette gradient, with the
// "· status slug" wordmark beside the graphic in muted text.
func Art(p Palette) string {
	body := strings.Join(ArtLines, "\n")
	painted := GradientText(body, p[GradLo], p[GradHi])
	word := lipgloss.NewStyle().Foreground(lipgloss.Color(p[Muted])).Render("  · status slug")
	lines := strings.Split(painted, "\n")
	if len(lines) > 1 {
		lines[1] += word
	}
	return strings.Join(lines, "\n")
}
