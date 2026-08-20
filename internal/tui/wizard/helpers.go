// Package wizard implements the mouse-native setup wizard.
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

// EnvCandidatesForProvider returns provider-specific key variable suggestions,
// most-specific first. The names are suggestions only; callers decide whether
// the variables are currently present.
func EnvCandidatesForProvider(providerName, presetName, kind, baseURL string) []string {
	hay := strings.ToUpper(providerName + " " + presetName + " " + kind + " " + baseURL)
	var out []string
	add := func(v string) {
		for _, existing := range out {
			if existing == v {
				return
			}
		}
		out = append(out, v)
	}
	switch {
	case strings.Contains(hay, "OPENROUTER"):
		add("OPENROUTER_API_KEY")
	case strings.Contains(hay, "ANTHROPIC"):
		add("ANTHROPIC_API_KEY")
	case strings.Contains(hay, "GEMINI") || strings.Contains(hay, "GOOGLE"):
		add("GEMINI_API_KEY")
		add("GOOGLE_API_KEY")
	case strings.Contains(hay, "GROQ"):
		add("GROQ_API_KEY")
	case strings.Contains(hay, "MISTRAL"):
		add("MISTRAL_API_KEY")
	case strings.Contains(hay, "DEEPSEEK"):
		add("DEEPSEEK_API_KEY")
	case strings.Contains(hay, "OPENAI"):
		add("OPENAI_API_KEY")
	}
	slug := strings.ToUpper(providerName)
	slug = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, slug)
	slug = strings.Trim(slug, "_")
	if slug != "" {
		add(slug + "_API_KEY")
	}
	return out
}

func defaultEnvName(d *wizardData) string {
	if candidates := EnvCandidatesForProvider(d.name, d.presetSel, d.kind, d.baseURL); len(candidates) > 0 {
		return candidates[0]
	}
	return "MY_PROVIDER_API_KEY"
}

// BuildProvider assembles a config.Provider from wizard step results.
func BuildProvider(name, label, kind, baseURL, probeURL, probeMode, keyRef, note string, models []config.Model, meters []config.Meter) config.Provider {
	return config.Provider{
		Name:      name,
		Label:     label,
		Kind:      kind,
		BaseURL:   strings.TrimRight(baseURL, "/"),
		ProbeURL:  strings.TrimRight(probeURL, "/"),
		ProbeMode: probeMode,
		KeyRef:    keyRef,
		Enabled:   true,
		Note:      note,
		Models:    models,
		Meters:    meters,
	}
}
