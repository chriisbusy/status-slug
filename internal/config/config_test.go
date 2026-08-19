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
		Name: "full", Panels: []string{"status", "usage", "favourites"},
		Arrangement: "grid", MainSplit: 0.66,
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
	if len(got.Views) != 1 || got.Views[0].Name != "full" || got.Views[0].MainSplit != 0.66 {
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

func TestConfigHomeOverride(t *testing.T) {
	t.Setenv("SSLUG_CONFIG_HOME", "/tmp/sslug-test-override")
	if got := config.Dir(); got != "/tmp/sslug-test-override" {
		t.Errorf("Dir: got %q", got)
	}
	if !strings.HasSuffix(config.Path(), "config.toml") {
		t.Errorf("Path: %q", config.Path())
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
