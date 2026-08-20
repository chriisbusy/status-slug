package wizard

import (
	"testing"

	"github.com/chriisbusy/status-slug/internal/config"
)

func TestReconfigureSeedsAttachCreditsAndCopiesSlices(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.Provider{{
		Name:    "OpenRouter",
		Kind:    "openai-compatible",
		BaseURL: "https://openrouter.ai/api/v1",
		Meters: []config.Meter{
			{Name: "Credits", Unit: "USD", Kind: "auto", Auto: "openrouter-credits"},
			{Name: "Spend", Unit: "USD", Kind: "manual"},
		},
		Models: []config.Model{{ID: "openrouter/auto", Favourite: true}},
	}}

	m := New(cfg, "OpenRouter")
	if !m.data.attachCredits {
		t.Fatal("existing openrouter credits meter did not seed attachCredits")
	}

	m.data.meters[0].Name = "Changed"
	m.data.models[0].ID = "changed-model"
	if cfg.Providers[0].Meters[0].Name != "Credits" {
		t.Fatalf("wizard meters alias original config slice: %+v", cfg.Providers[0].Meters)
	}
	if cfg.Providers[0].Models[0].ID != "openrouter/auto" {
		t.Fatalf("wizard models alias original config slice: %+v", cfg.Providers[0].Models)
	}
}

func TestAutoMeterHelpersDoNotMutateInput(t *testing.T) {
	meters := []config.Meter{
		{Name: "Credits", Unit: "USD", Kind: "auto", Auto: "openrouter-credits"},
		{Name: "Spend", Unit: "USD", Kind: "manual"},
	}

	removed := removeAutoMeter(meters, "openrouter-credits")
	if len(removed) != 1 || removed[0].Name != "Spend" {
		t.Fatalf("remove result: %+v", removed)
	}
	if meters[0].Name != "Credits" || meters[1].Name != "Spend" {
		t.Fatalf("removeAutoMeter mutated input backing array: %+v", meters)
	}

	upserted := upsertAutoMeter(meters, config.Meter{Name: "Credits2", Unit: "USD", Kind: "auto", Auto: "openrouter-credits"})
	if len(upserted) != 2 || upserted[0].Name != "Credits2" {
		t.Fatalf("upsert result: %+v", upserted)
	}
	if meters[0].Name != "Credits" {
		t.Fatalf("upsertAutoMeter mutated input backing array: %+v", meters)
	}
}
