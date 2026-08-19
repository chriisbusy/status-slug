// Package wizard implements the huh/v2 setup wizard.
// This file holds pure helpers separated from form plumbing.
package wizard

import (
	"fmt"
	"strings"

	"github.com/chriisbusy/status-slug/internal/config"
)

// MergeModels dedupes discovered + custom model IDs preserving order.
// Existing favourites are never dropped.
func MergeModels(discovered, custom []string, existing []config.Model) []config.Model {
	seen := map[string]bool{}
	var out []config.Model

	// Preserve existing models first (favourites stay).
	for _, m := range existing {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	// Then discovered.
	for _, id := range discovered {
		if !seen[id] {
			seen[id] = true
			out = append(out, config.Model{ID: id})
		}
	}
	// Then custom additions.
	for _, id := range custom {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, config.Model{ID: id, Favourite: true})
		}
	}
	return out
}

// KeyRef derives a key_ref string from the user's choice.
// source: "paste" | "env" | "file" | "none"
// value: the key material (paste), env var name (env), or file path (file).
// providerName: used as the keyring/file id.
// Returns the ref and the raw key material to store (empty for env/none).
func KeyRef(source, value, providerName string) (ref, material string, err error) {
	lower := strings.ToLower(providerName)
	id := strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, lower)

	switch source {
	case "paste":
		if value == "" {
			return "", "", fmt.Errorf("no key material provided")
		}
		return "keyring:" + id, value, nil
	case "env":
		if value == "" {
			return "", "", fmt.Errorf("no env var selected")
		}
		return "env:" + value, "", nil
	case "file":
		if value == "" {
			return "", "", fmt.Errorf("no file selected")
		}
		return "file:" + id, value, nil
	case "none":
		return "none", "", nil
	default:
		return "", "", fmt.Errorf("unknown key source %q", source)
	}
}

// DetectEnvVars scans os.Environ for likely API key variables.
func DetectEnvVars(environ []string) []string {
	patterns := []string{"_API_KEY", "_TOKEN", "OPENAI", "ANTHROPIC", "OPENROUTER", "GROQ", "MISTRAL", "DEEPSEEK", "GEMINI", "GOOGLE_API"}
	var out []string
	seen := map[string]bool{}
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		for _, pat := range patterns {
			if strings.Contains(k, pat) && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// BuildProvider assembles a config.Provider from wizard step results.
func BuildProvider(name, label, kind, baseURL, keyRef, note string, models []config.Model, meters []config.Meter) config.Provider {
	return config.Provider{
		Name:    name,
		Label:   label,
		Kind:    kind,
		BaseURL: strings.TrimRight(baseURL, "/"),
		KeyRef:  keyRef,
		Enabled: true,
		Note:    note,
		Models:  models,
		Meters:  meters,
	}
}
