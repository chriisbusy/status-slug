package wizard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/secret"
)

// Run executes the wizard. providerName is non-empty when reconfiguring.
// Returns the saved config (possibly with new providers) or an error.
func Run(cfg config.Config, providerName string) (config.Config, error) {
	for {
		p, keyMaterial, err := runOne(cfg, providerName)
		if err != nil {
			return cfg, err
		}
		if p == nil {
			break // user aborted
		}

		// Store the key.
		if keyMaterial != "" {
			if err := secret.Store(p.KeyRef, keyMaterial); err != nil {
				return cfg, fmt.Errorf("store key: %w", err)
			}
		}

		cfg.Upsert(*p)
		if err := config.Save(cfg); err != nil {
			return cfg, fmt.Errorf("save config: %w", err)
		}

		// Ask "add another?"
		var again bool
		form := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Add another provider?").
				Value(&again),
		))
		if err := form.Run(); err != nil {
			break
		}
		if !again {
			break
		}
		providerName = "" // fresh provider for next iteration
	}
	return cfg, nil
}

// runOne runs the wizard for a single provider and returns the built
// provider + any key material to store.
func runOne(cfg config.Config, reconfigure string) (*config.Provider, string, error) {
	var (
		name    = reconfigure
		label   = "official"
		kind    = "openai-compatible"
		baseURL string
		keySrc  string
		keyVal  string // paste material or env var name or file path
	)

	// Pre-fill from existing provider if reconfiguring.
	if reconfigure != "" {
		if existing := cfg.Find(reconfigure); existing != nil {
			label = existing.Label
			kind = existing.Kind
			baseURL = existing.BaseURL
		}
	}

	// --- Step 1: provider identity ---
	kindOptions := make([]huh.Option[string], 0, len(provider.Presets)+1)
	for _, p := range provider.Presets {
		kindOptions = append(kindOptions, huh.NewOption(p.Name, p.Kind+":"+p.BaseURL))
	}
	kindOptions = append(kindOptions, huh.NewOption("Custom", "custom:"))

	var kindSel string
	step1 := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Provider name").
			Value(&name).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("name required")
				}
				return nil
			}),
		huh.NewSelect[string]().
			Title("Label").
			Options(
				huh.NewOption("official", "official"),
				huh.NewOption("third-party", "third-party"),
				huh.NewOption("coding-plan", "coding-plan"),
				huh.NewOption("custom", "custom"),
			).
			Value(&label),
		huh.NewSelect[string]().
			Title("Kind / preset").
			Options(kindOptions...).
			Value(&kindSel),
		huh.NewInput().
			Title("Base URL").
			Value(&baseURL).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("base URL required")
				}
				return nil
			}),
	))
	if err := step1.Run(); err != nil {
		return nil, "", err
	}

	// Unpack kind selection.
	if kindSel != "" {
		k, u, _ := strings.Cut(kindSel, ":")
		kind = k
		if baseURL == "" {
			baseURL = u
		}
	}

	// --- Step 2: API key ---
	envVars := DetectEnvVars(os.Environ())
	keyOptions := []huh.Option[string]{
		huh.NewOption("Paste key now", "paste"),
		huh.NewOption("No key (local / none)", "none"),
	}
	for _, v := range envVars {
		keyOptions = append(keyOptions, huh.NewOption("env: "+v, "env:"+v))
	}

	step2 := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("API key source").
			Options(keyOptions...).
			Value(&keySrc),
	))
	if err := step2.Run(); err != nil {
		return nil, "", err
	}

	var keyRef, keyMaterial string

	switch {
	case keySrc == "none":
		keyRef = "none"
	case keySrc == "paste":
		step2b := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("Paste API key").
				Password(true).
				Value(&keyVal),
		))
		if err := step2b.Run(); err != nil {
			return nil, "", err
		}
		// Store to keyring; if unavailable, offer file fallback.
		if secret.KeyringAvailable() {
			keyRef = "keyring:" + strings.Map(func(r rune) rune {
				if 'a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '-' {
					return r
				}
				return '-'
			}, strings.ToLower(name))
			keyMaterial = keyVal
		} else {
			var fbChoice string
			fb := huh.NewForm(huh.NewGroup(
				huh.NewSelect[string]().
					Title("Keyring unavailable. Store key how?").
					Options(
						huh.NewOption("0600 file (plaintext at rest — warned)", "file"),
						huh.NewOption("Use env var reference instead", "env"),
						huh.NewOption("Abort", "abort"),
					).
					Value(&fbChoice),
			))
			if err := fb.Run(); err != nil || fbChoice == "abort" {
				return nil, "", nil
			}
			if fbChoice == "file" {
				keyRef = "file:" + strings.ToLower(name)
				keyMaterial = keyVal
			} else {
				// Ask for env var name.
				var envName string
				envForm := huh.NewForm(huh.NewGroup(
					huh.NewInput().Title("Environment variable name").Value(&envName),
				))
				if err := envForm.Run(); err != nil {
					return nil, "", err
				}
				keyRef = "env:" + envName
			}
		}
	case strings.HasPrefix(keySrc, "env:"):
		envVar := strings.TrimPrefix(keySrc, "env:")
		keyRef = "env:" + envVar
	}

	// --- Step 3: validate key (if we have one) ---
	if keyRef != "none" && keyRef != "" {
		resolvedKey, _ := secret.Resolve(keyRef)
		if resolvedKey == "" && keyMaterial != "" {
			resolvedKey = keyMaterial
		}
		if resolvedKey != "" {
			doer := check.NewDoer(8*time.Second, resolvedKey)
			adapter := provider.New(kind)
			res := adapter.Probe(context.Background(), doer, baseURL)
			msg := fmt.Sprintf("Probe result: %s — %s", res.Status, res.Reason)
			if res.Status != check.OK {
				var action string
				warnForm := huh.NewForm(huh.NewGroup(
					huh.NewSelect[string]().
						Title(msg).
						Options(
							huh.NewOption("Save anyway", "save"),
							huh.NewOption("Re-enter key", "retry"),
							huh.NewOption("Abort", "abort"),
						).
						Value(&action),
				))
				if err := warnForm.Run(); err != nil || action == "abort" {
					return nil, "", nil
				}
				if action == "retry" {
					return runOne(cfg, reconfigure) // restart wizard for this provider
				}
			}
		}
	}

	// --- Step 4: models ---
	var selectedModels []string
	var customModel string

	if keyRef != "none" && keyRef != "" {
		resolvedKey, _ := secret.Resolve(keyRef)
		if resolvedKey == "" && keyMaterial != "" {
			resolvedKey = keyMaterial
		}
		doer := check.NewDoer(8*time.Second, resolvedKey)
		ids, err := provider.ListModelsRaw(context.Background(), doer, kind, baseURL)
		if err == nil && len(ids) > 0 {
			modelOptions := make([]huh.Option[string], len(ids))
			for i, id := range ids {
				modelOptions[i] = huh.NewOption(id, id)
			}
			msForm := huh.NewForm(huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Favourite models (space to toggle)").
					Options(modelOptions...).
					Value(&selectedModels),
			))
			_ = msForm.Run()
		}
	}

	// Always offer custom model entry.
	customForm := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Add custom model ID (leave blank to skip)").
			Value(&customModel),
	))
	_ = customForm.Run()

	customModels := []string{}
	if strings.TrimSpace(customModel) != "" {
		customModels = []string{strings.TrimSpace(customModel)}
	}
	models := MergeModels(selectedModels, customModels, nil)
	// Mark favourites.
	for i := range models {
		for _, sel := range selectedModels {
			if models[i].ID == sel {
				models[i].Favourite = true
			}
		}
	}

	// --- Step 5: usage meters (optional loop) ---
	var meters []config.Meter
	for {
		var addMeter bool
		meterAsk := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Add a usage meter?").
				Value(&addMeter),
		))
		if err := meterAsk.Run(); err != nil || !addMeter {
			break
		}
		m, err := meterForm()
		if err != nil {
			break
		}
		meters = append(meters, *m)
	}

	// Auto-attach OpenRouter credits meter.
	if kind == "openai-compatible" && strings.Contains(baseURL, "openrouter.ai") {
		var attachCredits bool
		orForm := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Attach OpenRouter credits meter (auto)?").
				Value(&attachCredits),
		))
		if err := orForm.Run(); err == nil && attachCredits {
			meters = append(meters, config.Meter{
				Name: "Credits", Unit: "USD", Kind: "auto",
				Auto: "openrouter-credits", Reset: "never",
			})
		}
	}

	p := BuildProvider(name, label, kind, baseURL, keyRef, "", models, meters)
	return &p, keyMaterial, nil
}

// meterForm runs one meter definition dialog.
func meterForm() (*config.Meter, error) {
	var (
		name    string
		unit    = "USD"
		usedStr string
		capStr  string
		reset   = "never"
	)
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Meter name").Value(&name).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("name required")
			}
			return nil
		}),
		huh.NewInput().Title("Unit (e.g. USD, kWh, credits)").Value(&unit),
		huh.NewInput().Title("Current value (number, blank = 0)").Value(&usedStr),
		huh.NewInput().Title("Cap (number, blank = uncapped)").Value(&capStr),
		huh.NewSelect[string]().
			Title("Reset schedule").
			Options(
				huh.NewOption("Never", "never"),
				huh.NewOption("Monthly (day of month)", "monthly"),
				huh.NewOption("Weekly (day of week)", "weekly"),
				huh.NewOption("Fixed date", "date"),
			).
			Value(&reset),
	))
	if err := form.Run(); err != nil {
		return nil, err
	}

	m := &config.Meter{Name: name, Unit: unit, Kind: "manual"}
	fmt.Sscanf(usedStr, "%f", &m.Used)
	fmt.Sscanf(capStr, "%f", &m.Cap)

	switch reset {
	case "monthly":
		var day string
		df := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Day of month (1–31)").Value(&day),
		))
		if err := df.Run(); err != nil {
			return nil, err
		}
		m.Reset = "monthly:" + strings.TrimSpace(day)
	case "weekly":
		var day string
		df := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Day of week").
				Options(
					huh.NewOption("Monday", "mon"), huh.NewOption("Tuesday", "tue"),
					huh.NewOption("Wednesday", "wed"), huh.NewOption("Thursday", "thu"),
					huh.NewOption("Friday", "fri"), huh.NewOption("Saturday", "sat"),
					huh.NewOption("Sunday", "sun"),
				).
				Value(&day),
		))
		if err := df.Run(); err != nil {
			return nil, err
		}
		m.Reset = "weekly:" + day
	case "date":
		var d string
		df := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Date (YYYY-MM-DD)").Value(&d),
		))
		if err := df.Run(); err != nil {
			return nil, err
		}
		m.Reset = "date:" + strings.TrimSpace(d)
	default:
		m.Reset = "never"
	}
	return m, nil
}
