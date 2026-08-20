package theme_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/theme"
)

func TestBuiltinDistinct(t *testing.T) {
	for _, name := range theme.BuiltinNames() {
		p, _ := theme.Load(name, "")
		if name == "mono" {
			continue // mono is intentionally all-empty
		}
		if !theme.Distinct(p) {
			t.Errorf("builtin %q: ok/warn/err/unknown not pairwise distinct", name)
		}
	}
}

func TestMonoIsMono(t *testing.T) {
	p, _ := theme.Load("mono", "")
	if !theme.IsMono(p) {
		t.Error("mono palette should have all-empty roles")
	}
}

func TestUserThemeOverridesTwoRoles(t *testing.T) {
	dir := t.TempDir()
	content := "# my theme\nok = \"#AABBCC\"\naccent = \"#112233\"\n"
	if err := os.WriteFile(filepath.Join(dir, "mine.theme"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, warns := theme.Load("mine", dir)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if p[theme.OK] != "#AABBCC" {
		t.Errorf("ok: got %q", p[theme.OK])
	}
	if p[theme.Accent] != "#112233" {
		t.Errorf("accent: got %q", p[theme.Accent])
	}
	// Untouched role must fall back to sstop.
	sstop, _ := theme.Load("sstop", "")
	if p[theme.Err] != sstop[theme.Err] {
		t.Errorf("err should fall back to sstop: got %q", p[theme.Err])
	}
}

func TestUserThemeInvalidHexFallsBack(t *testing.T) {
	dir := t.TempDir()
	content := "ok = \"notacolor\"\n"
	os.WriteFile(filepath.Join(dir, "bad.theme"), []byte(content), 0o644)
	p, warns := theme.Load("bad", dir)
	if len(warns) == 0 {
		t.Error("expected warning for invalid hex")
	}
	sstop, _ := theme.Load("sstop", "")
	if p[theme.OK] != sstop[theme.OK] {
		t.Errorf("invalid hex should fall back: got %q", p[theme.OK])
	}
}

func TestUnknownThemeFallsBackToSstop(t *testing.T) {
	p, warns := theme.Load("doesnotexist", t.TempDir())
	if len(warns) == 0 {
		t.Error("expected warning for unknown theme")
	}
	sstop, _ := theme.Load("sstop", "")
	for _, r := range theme.AllRoles {
		if p[r] != sstop[r] {
			t.Errorf("role %s: got %q want sstop %q", r, p[r], sstop[r])
		}
	}
}

func TestNOColorForcedMono(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	p, warns := theme.LoadFromSettings(config.Settings{Theme: "sstop"})
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if !theme.IsMono(p) {
		t.Error("NO_COLOR should force mono palette")
	}
}

func TestBuiltinNames(t *testing.T) {
	names := theme.BuiltinNames()
	if len(names) != 8 {
		t.Errorf("builtin names: %v", names)
	}
}

// TestThemePartialValid: one invalid line must not abort the whole file (M28).
func TestThemePartialValid(t *testing.T) {
	dir := t.TempDir()
	content := "ok = \"#AABBCC\"\naccent = \"garbage\"\n"
	os.WriteFile(filepath.Join(dir, "partial.theme"), []byte(content), 0o644)
	p, warns := theme.Load("partial", dir)
	if len(warns) == 0 {
		t.Error("expected warning for invalid line")
	}
	if p[theme.OK] != "#AABBCC" {
		t.Errorf("valid role must still apply: got %q", p[theme.OK])
	}
	sstop, _ := theme.Load("sstop", "")
	if p[theme.Accent] != sstop[theme.Accent] {
		t.Errorf("invalid role must fall back: got %q", p[theme.Accent])
	}
}
