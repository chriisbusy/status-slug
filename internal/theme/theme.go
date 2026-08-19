// Package theme manages color palettes: builtins, user .theme files,
// per-role fallback, and degradation detection.
package theme

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/chriisbusy/status-slug/internal/config"
)

// Role is a named color slot in a palette.
type Role string

// All palette roles.
const (
	Bg             Role = "bg"
	Fg             Role = "fg"
	Muted          Role = "muted"
	Title          Role = "title"
	Accent         Role = "accent"
	BoxBorder      Role = "box_border"
	BoxBorderFocus Role = "box_border_focus"
	OK             Role = "ok"
	Warn           Role = "warn"
	Err            Role = "err"
	Unknown        Role = "unknown"
	BarFill        Role = "bar_fill"
	BarEmpty       Role = "bar_empty"
	SparkLo        Role = "spark_lo"
	SparkHi        Role = "spark_hi"
	GradLo         Role = "grad_lo"
	GradHi         Role = "grad_hi"
	SelectedBg     Role = "selected_bg"
	SelectedFg     Role = "selected_fg"
	KeyHint        Role = "key_hint"
)

// AllRoles is the canonical ordered role list.
var AllRoles = []Role{
	Bg, Fg, Muted, Title, Accent,
	BoxBorder, BoxBorderFocus,
	OK, Warn, Err, Unknown,
	BarFill, BarEmpty,
	SparkLo, SparkHi,
	GradLo, GradHi,
	SelectedBg, SelectedFg, KeyHint,
}

// Palette maps every role to a #RRGGBB string.
type Palette map[Role]string

// Warning is raised when a role or file had a problem and fell back.
type Warning struct {
	Message string
}

// Builtins.
// sstop is the default: btop-evoking — near-black background, cyan→magenta
// gradient chrome, vivid ok/warn/err, and a muted role that stays readable.
var builtinSstop = Palette{
	Bg:             "#0B0E14",
	Fg:             "#E5EDF5",
	Muted:          "#8B98A9",
	Title:          "#F2F6FA",
	Accent:         "#00D4FF",
	BoxBorder:      "#2E3A4A",
	BoxBorderFocus: "#00D4FF",
	OK:             "#00FF87",
	Warn:           "#FFBF40",
	Err:            "#FF4D5E",
	Unknown:        "#8B98A9",
	BarFill:        "#00D4FF",
	BarEmpty:       "#26303E",
	SparkLo:        "#26303E",
	SparkHi:        "#00FF87",
	GradLo:         "#00D4FF",
	GradHi:         "#FF5ED2",
	SelectedBg:     "#1B2735",
	SelectedFg:     "#F2F6FA",
	KeyHint:        "#8B98A9",
}

// mocha is the former default — Catppuccin-mocha-inspired, softer.
var builtinMocha = Palette{
	Bg:             "#1E1E2E",
	Fg:             "#CDD6F4",
	Muted:          "#8B9BC0",
	Title:          "#CDD6F4",
	Accent:         "#00C2FF",
	BoxBorder:      "#3B4250",
	BoxBorderFocus: "#00C2FF",
	OK:             "#00FF87",
	Warn:           "#FFD75F",
	Err:            "#FF5F5F",
	Unknown:        "#8B9BC0",
	BarFill:        "#00C2FF",
	BarEmpty:       "#3B4250",
	SparkLo:        "#3B4250",
	SparkHi:        "#00FF87",
	GradLo:         "#00C2FF",
	GradHi:         "#FF79C6",
	SelectedBg:     "#313244",
	SelectedFg:     "#CDD6F4",
	KeyHint:        "#8B9BC0",
}

var builtinNord = Palette{
	Bg:             "#2E3440",
	Fg:             "#D8DEE9",
	Muted:          "#4C566A",
	Title:          "#ECEFF4",
	Accent:         "#88C0D0",
	BoxBorder:      "#4C566A",
	BoxBorderFocus: "#88C0D0",
	OK:             "#A3BE8C",
	Warn:           "#EBCB8B",
	Err:            "#BF616A",
	Unknown:        "#4C566A",
	BarFill:        "#88C0D0",
	BarEmpty:       "#4C566A",
	SparkLo:        "#4C566A",
	SparkHi:        "#A3BE8C",
	GradLo:         "#88C0D0",
	GradHi:         "#B48EAD",
	SelectedBg:     "#434C5E",
	SelectedFg:     "#ECEFF4",
	KeyHint:        "#4C566A",
}

// builtinMono: no chromatic roles beyond fg/bg; all color roles fall back
// to fg or muted so nothing breaks on a stripped terminal.
var builtinMono = Palette{
	Bg:             "",
	Fg:             "",
	Muted:          "",
	Title:          "",
	Accent:         "",
	BoxBorder:      "",
	BoxBorderFocus: "",
	OK:             "",
	Warn:           "",
	Err:            "",
	Unknown:        "",
	BarFill:        "",
	BarEmpty:       "",
	SparkLo:        "",
	SparkHi:        "",
	GradLo:         "",
	GradHi:         "",
	SelectedBg:     "",
	SelectedFg:     "",
	KeyHint:        "",
}

var builtins = map[string]Palette{
	"sstop": builtinSstop,
	"mocha": builtinMocha,
	"nord":  builtinNord,
	"mono":  builtinMono,
}

// BuiltinNames returns the sorted builtin theme names.
func BuiltinNames() []string { return []string{"sstop", "mocha", "nord", "mono"} }

// Load resolves a theme name to a palette.
// name may be a builtin or the stem of a file in themesDir.
// Returns the palette plus any warnings (non-fatal).
func Load(name, themesDir string) (Palette, []Warning) {
	if name == "" {
		name = "sstop"
	}
	if p, ok := builtins[name]; ok {
		return clonePalette(p), nil
	}
	// Try user file.
	path := filepath.Join(themesDir, name+".theme")
	data, err := os.ReadFile(path)
	if err != nil {
		w := []Warning{{Message: fmt.Sprintf("theme %q not found, using sstop", name)}}
		return clonePalette(builtinSstop), w
	}
	p, warns := parseUserTheme(string(data))
	return p, warns
}

// parseUserTheme parses a .theme file, layering valid roles over sstop.
func parseUserTheme(content string) (Palette, []Warning) {
	base := clonePalette(builtinSstop)
	var warns []Warning
	for lineNo, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			warns = append(warns, Warning{fmt.Sprintf("theme line %d: no '='", lineNo+1)})
			continue
		}
		role := Role(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		// Strip optional surrounding quotes.
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if !validRole(role) {
			warns = append(warns, Warning{fmt.Sprintf("theme line %d: unknown role %q", lineNo+1, string(role))})
			continue
		}
		if !validHex(val) {
			warns = append(warns, Warning{fmt.Sprintf("theme line %d: invalid color %q for role %s", lineNo+1, val, string(role))})
			continue
		}
		base[role] = val
	}
	return base, warns
}

// validRole reports whether r is a known role.
func validRole(r Role) bool {
	for _, known := range AllRoles {
		if r == known {
			return true
		}
	}
	return false
}

// validHex reports whether s is a #RRGGBB string (or empty for mono).
func validHex(s string) bool {
	if s == "" {
		return true // mono: empty = no color
	}
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// Distinct reports whether ok/warn/err/unknown are pairwise distinct in p.
func Distinct(p Palette) bool {
	vals := []string{p[OK], p[Warn], p[Err], p[Unknown]}
	seen := map[string]bool{}
	for _, v := range vals {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// IsMono reports whether p carries no color information.
func IsMono(p Palette) bool {
	for _, r := range AllRoles {
		if p[r] != "" {
			return false
		}
	}
	return true
}

// NO_COLOR reports whether the environment forces mono output.
func NO_COLOR() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return true
	}
	term := os.Getenv("TERM")
	return term == "dumb" || term == ""
}

// LoadFromSettings loads the theme named in settings, respecting NO_COLOR,
// the terminal's background (light terminals get terminal-native mono so
// nothing renders invisible), and theme_background (bg role emptied when
// false → terminal's own background shows through, btop-style).
func LoadFromSettings(s config.Settings) (Palette, []Warning) {
	return LoadForTerminal(s, termIsDark())
}

// LoadForTerminal is LoadFromSettings with an injectable darkness flag for tests.
func LoadForTerminal(s config.Settings, dark bool) (Palette, []Warning) {
	if NO_COLOR() {
		return clonePalette(builtinMono), nil
	}
	if !dark {
		// Light terminal: dark-scheme colors are unreadable there; fall back
		// to terminal-native mono rather than render invisible text.
		p := clonePalette(builtinMono)
		return p, []Warning{{Message: "light terminal detected — using terminal-native colors"}}
	}
	name := s.Theme
	if name == "" {
		name = "sstop"
	}
	p, warns := Load(name, config.ThemesDir())
	if !s.ThemeBackground {
		p[Bg] = "" // terminal-native background
	}
	return p, warns
}

// termIsDark reports whether the terminal has a dark background.
// Non-TTY (tests, pipes) assumes dark — the common case.
func termIsDark() bool {
	if !term.IsTerminal(os.Stdout.Fd()) {
		return true
	}
	return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
}

func clonePalette(p Palette) Palette {
	out := make(Palette, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}
