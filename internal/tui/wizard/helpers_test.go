package wizard_test

import (
	"testing"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/tui/wizard"
)

func TestMergeModelsDedupe(t *testing.T) {
	existing := []config.Model{
		{ID: "gpt-5", Favourite: true},
	}
	discovered := []string{"gpt-5", "gpt-5-mini", "claude-opus-4-1"}
	custom := []string{"gpt-5-mini", "my-custom-model"}

	got := wizard.MergeModels(discovered, custom, existing)
	ids := make([]string, len(got))
	for i, m := range got {
		ids[i] = m.ID
	}
	// gpt-5 (existing) first, then discovered deduped, then custom deduped.
	want := []string{"gpt-5", "gpt-5-mini", "claude-opus-4-1", "my-custom-model"}
	if len(ids) != len(want) {
		t.Fatalf("got %v want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("position %d: got %q want %q", i, ids[i], id)
		}
	}
	// Existing favourite must be preserved.
	if !got[0].Favourite {
		t.Error("existing favourite lost in merge")
	}
	// Custom additions are auto-favourited.
	if !got[3].Favourite {
		t.Error("custom model not marked favourite")
	}
}

func TestKeyRefDerivation(t *testing.T) {
	cases := []struct {
		source, value, name string
		wantRef             string
		wantMaterial        string
		wantErr             bool
	}{
		{"paste", "sk-key123", "My Provider", "keyring:my-provider", "sk-key123", false},
		{"env", "OPENAI_API_KEY", "OpenAI", "env:OPENAI_API_KEY", "", false},
		{"file", "/path/to/key", "Test", "file:test", "/path/to/key", false},
		{"none", "", "Local", "none", "", false},
		{"paste", "", "X", "", "", true}, // no material
		{"env", "", "X", "", "", true},   // no var
		{"bogus", "", "X", "", "", true}, // unknown source
	}
	for _, tc := range cases {
		ref, material, err := wizard.KeyRef(tc.source, tc.value, tc.name)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error", tc.source)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.source, err)
			continue
		}
		if ref != tc.wantRef {
			t.Errorf("%s: ref got %q want %q", tc.source, ref, tc.wantRef)
		}
		if material != tc.wantMaterial {
			t.Errorf("%s: material got %q want %q", tc.source, material, tc.wantMaterial)
		}
	}
}

func TestDetectEnvVars(t *testing.T) {
	environ := []string{
		"OPENAI_API_KEY=sk-x",
		"ANTHROPIC_API_KEY=sk-y",
		"HOME=/home/u",
		"PATH=/usr/bin",
		"MY_TOKEN=tok",
		"GROQ_SECRET=z",
	}
	got := wizard.DetectEnvVars(environ)
	found := map[string]bool{}
	for _, v := range got {
		found[v] = true
	}
	for _, want := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "MY_TOKEN"} {
		if !found[want] {
			t.Errorf("missing %q in detected env vars %v", want, got)
		}
	}
	if found["HOME"] || found["PATH"] {
		t.Errorf("should not detect HOME/PATH: %v", got)
	}
}

func TestEnvCandidatesForProvider(t *testing.T) {
	got := wizard.EnvCandidatesForProvider("OpenRouter", "OpenRouter", "openai-compatible", "https://openrouter.ai/api/v1")
	if len(got) == 0 || got[0] != "OPENROUTER_API_KEY" {
		t.Fatalf("OpenRouter candidate first: %v", got)
	}
	got = wizard.EnvCandidatesForProvider("Google Gemini", "Google Gemini", "google", "https://generativelanguage.googleapis.com")
	found := map[string]bool{}
	for _, v := range got {
		found[v] = true
	}
	if !found["GEMINI_API_KEY"] || !found["GOOGLE_API_KEY"] {
		t.Fatalf("Google candidates: %v", got)
	}
}

func TestBuildProvider(t *testing.T) {
	p := wizard.BuildProvider("Test", "official", "openai-compatible",
		"https://api.example.com/v1/", "https://health.example.com/ping/", "chat", "keyring:test", "my note",
		[]config.Model{{ID: "m1", Favourite: true}},
		[]config.Meter{{Name: "Spend", Unit: "USD", Kind: "manual"}},
	)
	if p.Name != "Test" {
		t.Errorf("name: %q", p.Name)
	}
	// Trailing slash stripped.
	if p.BaseURL != "https://api.example.com/v1" {
		t.Errorf("base_url: %q", p.BaseURL)
	}
	if p.ProbeURL != "https://health.example.com/ping" {
		t.Errorf("probe_url: %q", p.ProbeURL)
	}
	if p.ProbeMode != "chat" {
		t.Errorf("probe_mode: %q", p.ProbeMode)
	}
	if !p.Enabled {
		t.Error("should be enabled")
	}
	if len(p.Models) != 1 || !p.Models[0].Favourite {
		t.Errorf("models: %+v", p.Models)
	}
	if len(p.Meters) != 1 || p.Meters[0].Name != "Spend" {
		t.Errorf("meters: %+v", p.Meters)
	}
}
