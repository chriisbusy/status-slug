package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chriisbusy/status-slug/internal/config"
)

func TestRoundtrip(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.Provider{{
		Name:    "TestProv",
		Label:   "official",
		Kind:    "openai-compatible",
		BaseURL: "https://example.com/v1",
		KeyRef:  "env:TEST_KEY",
		Enabled: true,
		Note:    "rate limit 60rpm",
		Meters: []config.Meter{{
			Name: "Energy", Unit: "kWh", Kind: "manual",
			Used: 231.5, Cap: 1000.0, Reset: "monthly:1",
		}},
		Models: []config.Model{{
			ID: "gpt-5-mini", Alias: "mini", Favourite: true, Probe: "chat",
		}},
	}}
	cfg.Views = []config.View{{
		Name: "full", Panels: []string{"status", "stats", "usage", "favourites"},
		TopRatio: 0.35, LeftRatio: 0.40, UsageRatio: 0.45,
	}}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.SaveTo(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	// File must be 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode: got %o want 600", perm)
	}

	got, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got.Version != 1 {
		t.Errorf("version: got %d", got.Version)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("providers: got %d", len(got.Providers))
	}
	p := got.Providers[0]
	if p.Name != "TestProv" || p.BaseURL != "https://example.com/v1" {
		t.Errorf("provider: %+v", p)
	}
	if len(p.Meters) != 1 || p.Meters[0].Name != "Energy" || p.Meters[0].Used != 231.5 {
		t.Errorf("meters: %+v", p.Meters)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "gpt-5-mini" || !p.Models[0].Favourite {
		t.Errorf("models: %+v", p.Models)
	}
	if len(got.Views) != 1 || got.Views[0].Name != "full" ||
		got.Views[0].TopRatio != 0.35 || got.Views[0].LeftRatio != 0.40 || got.Views[0].UsageRatio != 0.45 {
		t.Errorf("views: %+v", got.Views)
	}
}

func TestLoadFromMissing(t *testing.T) {
	cfg, err := config.LoadFrom(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.Settings.Theme != "sstop" {
		t.Errorf("default theme: got %q", cfg.Settings.Theme)
	}
}

func TestViewAndGraphDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 1\n\n[settings]\ntheme = \"sstop\"\n\n[[views]]\nname = \"legacy\"\npanels = [\"status\", \"stats\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.GraphStyle != "tty" {
		t.Fatalf("graph style default = %q, want tty", got.Settings.GraphStyle)
	}
	if len(got.Views) != 1 || got.Views[0].TopRatio != 0.33 || got.Views[0].LeftRatio != 0.37 || got.Views[0].UsageRatio != 0.46 {
		t.Fatalf("normalized view = %+v", got.Views)
	}
}

func TestLegacyGraphGlyphsMigrates(t *testing.T) {
	for legacy, want := range map[string]string{"braille": "braille", "blocks": "block", "ascii": "tty"} {
		t.Run(legacy, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			body := "version = 1\n\n[settings]\ngraph_glyphs = \"" + legacy + "\"\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := config.LoadFrom(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Settings.GraphStyle != want {
				t.Fatalf("legacy %q migrated to %q, want %q", legacy, got.Settings.GraphStyle, want)
			}
		})
	}
}

func TestLegacyViewLayoutMigratesToRatios(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `version = 1

[[views]]
name = "wide"
panels = ["status", "stats", "usage"]
arrangement = "grid"
main_split = 0.65

[[views]]
name = "compact"
panels = ["status", "stats", "usage", "favourites"]
arrangement = "stack"
compact = true

[[views]]
name = "edge"
panels = ["status", "stats"]
arrangement = "grid"
main_split = 0.80
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Views[0].LeftRatio != 0.65 || got.Views[0].TopRatio != 0.33 || got.Views[0].UsageRatio != 0.46 {
		t.Fatalf("legacy grid migration = %+v", got.Views[0])
	}
	if got.Views[1].TopRatio != 0.40 || got.Views[1].LeftRatio != 0.50 || got.Views[1].UsageRatio != 0.50 {
		t.Fatalf("legacy stack migration = %+v", got.Views[1])
	}
	if got.Views[2].LeftRatio != 0.75 {
		t.Fatalf("legacy 0.80 split should clamp to new maximum: %+v", got.Views[2])
	}
}

func TestConfigHomeOverride(t *testing.T) {
	t.Setenv("SSLUG_CONFIG_HOME", "/tmp/sslug-test-override")
	if got := config.Dir(); got != "/tmp/sslug-test-override" {
		t.Errorf("Dir: got %q", got)
	}
	if !strings.HasSuffix(config.Path(), "config.toml") {
		t.Errorf("Path: %q", config.Path())
	}
}

// TestAtomicSaveLeavesNoTemp: after a save, no .tmp-* files remain in the
// config dir and the file has final content (M13).
func TestAtomicSaveLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.Providers = []config.Provider{{Name: "X", Enabled: true}}
	if err := config.SaveTo(path, cfg); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	// Saved content must parse back to the same provider list.
	got, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 || got.Providers[0].Name != "X" {
		t.Errorf("roundtrip after atomic save: %+v", got.Providers)
	}
}

func TestXDGConfigHome(t *testing.T) {
	t.Setenv("SSLUG_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	got := config.Dir()
	if got != "/tmp/xdg-test/status-slug" {
		t.Errorf("Dir: got %q", got)
	}
}

func TestUpsertRemove(t *testing.T) {
	cfg := config.Default()
	cfg.Upsert(config.Provider{Name: "A", Enabled: true})
	cfg.Upsert(config.Provider{Name: "A", Enabled: false}) // replace
	if len(cfg.Providers) != 1 {
		t.Fatalf("upsert duplicated: %d", len(cfg.Providers))
	}
	if cfg.Providers[0].Enabled {
		t.Error("upsert did not replace")
	}
	if !cfg.Remove("A") {
		t.Error("Remove returned false for existing provider")
	}
	if cfg.Remove("A") {
		t.Error("Remove returned true for missing provider")
	}
}
