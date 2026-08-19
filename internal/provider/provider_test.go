package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/state"
)

func newServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *check.Doer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, check.NewDoer(5*time.Second, "test-key")
}

func TestOpenAIListModels(t *testing.T) {
	srv, d := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-5"},{"id":"gpt-5-mini"}]}`)
	})
	ids, err := provider.ListModelsRaw(context.Background(), d, "openai-compatible", srv.URL+"/v1")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(ids) != 2 || ids[0] != "gpt-5" || ids[1] != "gpt-5-mini" {
		t.Errorf("ids: %v", ids)
	}
}

func TestAnthropicListModels(t *testing.T) {
	srv, d := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"claude-opus-4-1"},{"id":"claude-sonnet-4-5"}]}`)
	})
	ids, err := provider.ListModelsRaw(context.Background(), d, "anthropic", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids: %v", ids)
	}
}

func TestGoogleListModels(t *testing.T) {
	srv, d := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"name":"models/gemini-2.0-flash"},{"name":"models/gemini-2.0-pro"}]}`)
	})
	ids, err := provider.ListModelsRaw(context.Background(), d, "google", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(ids) != 2 || ids[0] != "gemini-2.0-flash" {
		t.Errorf("ids (should strip models/ prefix): %v", ids)
	}
}

func TestProbeModelPostsMaxTokens1(t *testing.T) {
	var gotBody []byte
	srv, d := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = make([]byte, r.ContentLength)
		r.Body.Read(gotBody)
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[]}`)
	})
	adapter := provider.New("openai-compatible")
	r := adapter.ProbeModel(context.Background(), d, srv.URL+"/v1", "gpt-5-mini")
	if r.Status != check.OK {
		t.Fatalf("probe: %v", r)
	}
	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("unmarshal posted body: %v", err)
	}
	if body["max_tokens"] != float64(1) {
		t.Errorf("max_tokens: got %v want 1", body["max_tokens"])
	}
	if body["model"] != "gpt-5-mini" {
		t.Errorf("model: got %v", body["model"])
	}
}

func TestOpenRouterCreditsAdapter(t *testing.T) {
	srv, d := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/credits" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"total_credits":100.0,"total_usage":21.8}}`)
	})
	// baseURL is the API root; credits adapter appends /credits.
	base := srv.URL + "/api/v1"
	adapter := provider.New("openai-compatible")
	ur, err := adapter.FetchUsage(context.Background(), d, base, "openrouter-credits")
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if ur.Unit != "USD" {
		t.Errorf("unit: got %q", ur.Unit)
	}
	want := 100.0 - 21.8
	if ur.Value != want {
		t.Errorf("value: got %v want %v", ur.Value, want)
	}
	if ur.Cap != 100.0 {
		t.Errorf("cap: got %v", ur.Cap)
	}
	if ur.FetchedAt.IsZero() {
		t.Error("fetched_at is zero")
	}
}

func TestPresetLookup(t *testing.T) {
	p := provider.FindPreset("OpenAI")
	if p == nil || p.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("OpenAI preset: %+v", p)
	}
	if provider.FindPreset("NonExistent") != nil {
		t.Error("expected nil for unknown preset")
	}
}

func TestMoshiBuild(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.Provider{{
		Name:    "Neuralwatt",
		Label:   "custom",
		Kind:    "custom",
		BaseURL: "http://localhost:18821/v1",
		Enabled: true,
		Meters: []config.Meter{{
			Name: "Energy", Unit: "kWh", Kind: "manual",
			Used: 231.5, Cap: 1000.0, Reset: "monthly:1",
		}},
	}}
	st := state.New()
	st.SetMeter("Neuralwatt", "Energy", 231.5)

	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snaps := provider.MoshiBuild(cfg, st, now)
	if len(snaps) != 1 {
		t.Fatalf("snaps: got %d want 1", len(snaps))
	}
	s := snaps[0]
	if s.AccountID != "sslug:Neuralwatt" {
		t.Errorf("accountId: got %q", s.AccountID)
	}
	if s.Agent != "sslug" {
		t.Errorf("agent: got %q", s.Agent)
	}
	if len(s.Windows) != 1 {
		t.Fatalf("windows: got %d", len(s.Windows))
	}
	w := s.Windows[0]
	if w.Label != "Energy" {
		t.Errorf("window label: got %q", w.Label)
	}
	wantPct := 231.5 / 1000.0 * 100
	if w.UsedPercentage < wantPct-0.01 || w.UsedPercentage > wantPct+0.01 {
		t.Errorf("usedPercentage: got %v want ~%v", w.UsedPercentage, wantPct)
	}
	// monthly:1 from 2026-08-19 → next reset 2026-09-01.
	if w.ResetsAt.Month() != 9 || w.ResetsAt.Day() != 1 {
		t.Errorf("resetsAt: got %v want September 1", w.ResetsAt)
	}
	// USD meter → cost populated.
	cfg2 := config.Default()
	cfg2.Providers = []config.Provider{{
		Name: "OpenRouter", Kind: "openai-compatible", Enabled: true,
		Meters: []config.Meter{{Name: "Credits", Unit: "USD", Kind: "auto", Used: 78.2, Cap: 100.0, Reset: "never"}},
	}}
	st2 := state.New()
	st2.SetMeter("OpenRouter", "Credits", 78.2)
	snaps2 := provider.MoshiBuild(cfg2, st2, now)
	if len(snaps2) != 1 || snaps2[0].Cost == nil {
		t.Fatal("expected cost for USD meter")
	}
	if snaps2[0].Cost.Used != 78.2 || snaps2[0].Cost.Limit != 100.0 {
		t.Errorf("cost: %+v", snaps2[0].Cost)
	}
}

func TestNextResetMonthly(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	// monthly:1 → September 1
	got := provider.NextResetForTest("monthly:1", now)
	if got.Month() != 9 || got.Day() != 1 {
		t.Errorf("monthly:1 from Aug 19 → got %v", got)
	}
	// monthly:19 on the 19th → next month (must be strictly after now)
	got = provider.NextResetForTest("monthly:19", now)
	if !got.After(now) {
		t.Errorf("monthly:19 on Aug 19 should be in future, got %v", got)
	}
	// monthly:31 in February → clamps to Feb 28/29.
	feb := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got = provider.NextResetForTest("monthly:31", feb)
	if got.Month() > 3 {
		t.Errorf("monthly:31 from Feb 1 → got %v (should not skip to April)", got)
	}
}

func TestNextResetWeekly(t *testing.T) {
	// Wednesday Aug 19 2026.
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	got := provider.NextResetForTest("weekly:mon", now)
	if got.Weekday() != time.Monday {
		t.Errorf("weekly:mon → got %v (weekday %v)", got, got.Weekday())
	}
	if !got.After(now) {
		t.Errorf("weekly:mon must be in future")
	}
}

func TestNextResetDate(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	got := provider.NextResetForTest("date:2026-09-01", now)
	if got.Month() != 9 || got.Day() != 1 {
		t.Errorf("date:2026-09-01 → got %v", got)
	}
}
