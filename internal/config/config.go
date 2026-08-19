// Package config defines the TOML config model, XDG path resolution, and
// atomic save for status-slug.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Config is the root of config.toml.
type Config struct {
	Version   int        `toml:"version"`
	Settings  Settings   `toml:"settings"`
	Views     []View     `toml:"views,omitempty"`
	Providers []Provider `toml:"providers,omitempty"`
}

// Settings holds user-tunable preferences.
type Settings struct {
	Theme         string `toml:"theme"`
	ProbeTimeout  int    `toml:"probe_timeout_seconds"`
	AutoRefresh   int    `toml:"auto_refresh_seconds"`
	ProbeMode     string `toml:"probe_mode"`
	HistoryLength int    `toml:"history_length"`
	KeysSource    string `toml:"keys_source"`
	NerdFont      bool   `toml:"nerd_font"`
	ConfirmQuit   bool   `toml:"confirm_quit"`
	CheckOnLaunch bool   `toml:"check_on_launch"`
	AlertBell     bool   `toml:"alert_bell"`
	BorderStyle   string `toml:"border_style"`
	GraphGlyphs   string `toml:"graph_glyphs"`
}

// View is a named panel arrangement preset.
type View struct {
	Name        string   `toml:"name"`
	Panels      []string `toml:"panels"`
	Arrangement string   `toml:"arrangement"`
	Compact     bool     `toml:"compact"`
	MainSplit   float64  `toml:"main_split"`
}

// Provider is one configured LLM provider.
type Provider struct {
	Name      string  `toml:"name"`
	Label     string  `toml:"label"`
	Kind      string  `toml:"kind"`
	BaseURL   string  `toml:"base_url"`
	KeyRef    string  `toml:"key_ref"`
	Enabled   bool    `toml:"enabled"`
	ProbeMode string  `toml:"probe_mode,omitempty"`
	Note      string  `toml:"note,omitempty"`
	Meters    []Meter `toml:"meters,omitempty"`
	Models    []Model `toml:"models,omitempty"`
}

// Meter is a user-defined usage meter attached to a provider.
type Meter struct {
	Name  string  `toml:"name"`
	Unit  string  `toml:"unit"`
	Kind  string  `toml:"kind"`            // manual | auto
	Auto  string  `toml:"auto,omitempty"`  // adapter id when kind=auto
	Used  float64 `toml:"used,omitempty"`  // initial value
	Cap   float64 `toml:"cap,omitempty"`   // 0 = uncapped
	Reset string  `toml:"reset,omitempty"` // monthly:N | weekly:day | date:YYYY-MM-DD | never
}

// Model is a provider model entry.
type Model struct {
	ID        string `toml:"id"`
	Alias     string `toml:"alias,omitempty"`
	Favourite bool   `toml:"favourite"`
	Probe     string `toml:"probe,omitempty"` // models|chat; empty = inherit
}

// Default returns a Config populated with defaults, no providers.
func Default() Config {
	return Config{
		Version: 1,
		Settings: Settings{
			Theme:         "sstop",
			ProbeTimeout:  10,
			AutoRefresh:   60,
			ProbeMode:     "models",
			HistoryLength: 60,
			KeysSource:    "auto",
			BorderStyle:   "rounded",
			GraphGlyphs:   "braille",
		},
	}
}

// Dir returns the config directory: $SSLUG_CONFIG_HOME, else
// $XDG_CONFIG_HOME/status-slug, else ~/.config/status-slug.
func Dir() string {
	if d := os.Getenv("SSLUG_CONFIG_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "status-slug")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "status-slug")
}

// Path returns the full path to config.toml.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// ThemesDir returns the user themes directory.
func ThemesDir() string { return filepath.Join(Dir(), "themes") }

// Load reads and parses config.toml. Returns Default with no error if the
// file does not exist.
func Load() (Config, error) {
	return LoadFrom(Path())
}

// LoadFrom parses the given TOML file.
func LoadFrom(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg atomically (temp file + rename, mode 0600).
func Save(cfg Config) error { return SaveTo(Path(), cfg) }

// SaveTo writes cfg to path atomically with mode 0600.
func SaveTo(path string, cfg Config) error {
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return writeAtomic(path, data)
}

// writeAtomic writes data to path via temp file + rename, mode 0600.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Find returns the provider with the given name, or nil.
func (c *Config) Find(name string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}

// FindView returns the named user view, or nil.
func (c *Config) FindView(name string) *View {
	for i := range c.Views {
		if c.Views[i].Name == name {
			return &c.Views[i]
		}
	}
	return nil
}

// Upsert replaces or appends a provider by name.
func (c *Config) Upsert(p Provider) {
	for i := range c.Providers {
		if c.Providers[i].Name == p.Name {
			c.Providers[i] = p
			return
		}
	}
	c.Providers = append(c.Providers, p)
}

// Remove deletes the named provider. Returns false if not found.
func (c *Config) Remove(name string) bool {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			c.Providers = append(c.Providers[:i], c.Providers[i+1:]...)
			return true
		}
	}
	return false
}

// String renders the config back to TOML (for tests and debugging).
func (c Config) String() string {
	var buf bytes.Buffer
	_ = toml.NewEncoder(&buf).Encode(c)
	return buf.String()
}
