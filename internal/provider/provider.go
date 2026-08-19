// Package provider defines presets, probe adapters, and the moshi formatter.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
)

// Preset is a known provider template.
type Preset struct {
	Name    string
	Kind    string // openai-compatible|anthropic|google|custom
	BaseURL string
}

// Presets is the built-in provider table.
var Presets = []Preset{
	{Name: "OpenAI", Kind: "openai-compatible", BaseURL: "https://api.openai.com/v1"},
	{Name: "OpenRouter", Kind: "openai-compatible", BaseURL: "https://openrouter.ai/api/v1"},
	{Name: "Anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com"},
	{Name: "Google Gemini", Kind: "google", BaseURL: "https://generativelanguage.googleapis.com"},
	{Name: "Groq", Kind: "openai-compatible", BaseURL: "https://api.groq.com/openai/v1"},
	{Name: "DeepSeek", Kind: "openai-compatible", BaseURL: "https://api.deepseek.com/v1"},
	{Name: "Mistral", Kind: "openai-compatible", BaseURL: "https://api.mistral.ai/v1"},
	{Name: "Ollama", Kind: "openai-compatible", BaseURL: "http://localhost:11434/v1"},
}

// FindPreset returns the preset by name, or nil.
func FindPreset(name string) *Preset {
	for i := range Presets {
		if strings.EqualFold(Presets[i].Name, name) {
			return &Presets[i]
		}
	}
	return nil
}

// Adapter is the per-kind probe/list/usage interface.
// Methods take the full provider config so kind-specific options
// (probe_url, probe_mode) are honored.
type Adapter interface {
	// Probe checks provider-level health.
	Probe(ctx context.Context, doer *check.Doer, p config.Provider) check.Result
	// ProbeModel checks one model with a minimal chat completion.
	ProbeModel(ctx context.Context, doer *check.Doer, p config.Provider, modelID string) check.Result
	// ListModels returns available model IDs.
	ListModels(ctx context.Context, doer *check.Doer, p config.Provider) ([]string, error)
	// FetchUsage fetches usage data for auto meters. Returns nil if unsupported.
	FetchUsage(ctx context.Context, doer *check.Doer, p config.Provider, autoID string) (*UsageResult, error)
}

// UsageResult is the output of an auto-usage adapter.
type UsageResult struct {
	Value     float64
	Unit      string
	Cap       float64 // 0 = uncapped
	FetchedAt time.Time
}

// New returns the Adapter for kind. Unknown kinds fall back to openai-compatible.
func New(kind string) Adapter {
	switch kind {
	case "anthropic":
		return anthropicAdapter{}
	case "google":
		return googleAdapter{}
	default:
		return openaiAdapter{}
	}
}

// --- openai-compatible ---

type openaiAdapter struct{}

func (a openaiAdapter) Probe(ctx context.Context, d *check.Doer, p config.Provider) check.Result {
	url := p.ProbeURL
	if url == "" {
		url = p.BaseURL + "/models"
	}
	return d.Get(ctx, url)
}

func (a openaiAdapter) ProbeModel(ctx context.Context, d *check.Doer, p config.Provider, modelID string) check.Result {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`, modelID)
	return d.Post(ctx, p.BaseURL+"/chat/completions", []byte(body))
}

func (a openaiAdapter) ListModels(ctx context.Context, d *check.Doer, p config.Provider) ([]string, error) {
	return ListModelsRaw(ctx, d, "openai-compatible", p.BaseURL)
}

func (a openaiAdapter) FetchUsage(ctx context.Context, d *check.Doer, p config.Provider, autoID string) (*UsageResult, error) {
	if autoID == "openrouter-credits" {
		return fetchOpenRouterCredits(ctx, d, p.BaseURL)
	}
	return nil, fmt.Errorf("unknown auto usage adapter %q", autoID)
}

// --- anthropic ---

type anthropicAdapter struct{}

func (a anthropicAdapter) auth(d *check.Doer) *check.Doer {
	d.AuthHeader = "x-api-key"
	return d
}

func (a anthropicAdapter) Probe(ctx context.Context, d *check.Doer, p config.Provider) check.Result {
	url := p.ProbeURL
	if url == "" {
		url = p.BaseURL + "/v1/models"
	}
	return a.auth(d).Do(ctx, "GET", url, nil, map[string]string{
		"anthropic-version": "2023-06-01",
	})
}

func (a anthropicAdapter) ProbeModel(ctx context.Context, d *check.Doer, p config.Provider, modelID string) check.Result {
	body := fmt.Sprintf(`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`, modelID)
	return a.auth(d).Do(ctx, "POST", p.BaseURL+"/v1/messages", []byte(body), map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": "2023-06-01",
	})
}

func (a anthropicAdapter) ListModels(ctx context.Context, d *check.Doer, p config.Provider) ([]string, error) {
	return ListModelsRaw(ctx, a.auth(d), "anthropic", p.BaseURL)
}

func (a anthropicAdapter) FetchUsage(_ context.Context, _ *check.Doer, _ config.Provider, _ string) (*UsageResult, error) {
	return nil, nil
}

// --- google ---

// googleAdapter sends the key via the x-goog-api-key header — never in the
// URL, which proxies and access logs would record.
type googleAdapter struct{}

func (a googleAdapter) auth(d *check.Doer) *check.Doer {
	d.AuthHeader = "x-goog-api-key"
	return d
}

func (a googleAdapter) Probe(ctx context.Context, d *check.Doer, p config.Provider) check.Result {
	url := p.ProbeURL
	if url == "" {
		url = p.BaseURL + "/v1beta/models"
	}
	return a.auth(d).Get(ctx, url)
}

func (a googleAdapter) ProbeModel(ctx context.Context, d *check.Doer, p config.Provider, modelID string) check.Result {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", p.BaseURL, modelID)
	body := `{"contents":[{"parts":[{"text":"ping"}]}],"generationConfig":{"maxOutputTokens":1}}`
	return a.auth(d).Post(ctx, url, []byte(body))
}

func (a googleAdapter) ListModels(ctx context.Context, d *check.Doer, p config.Provider) ([]string, error) {
	return ListModelsRaw(ctx, a.auth(d), "google", p.BaseURL)
}

func (a googleAdapter) FetchUsage(_ context.Context, _ *check.Doer, _ config.Provider, _ string) (*UsageResult, error) {
	return nil, nil
}

// --- raw model listing (returns parsed model IDs from response bodies) ---

// ListModelsRaw fetches and parses model IDs for any adapter kind.
// It uses the raw HTTP response body rather than check.Result.
func ListModelsRaw(ctx context.Context, d *check.Doer, kind, baseURL string) ([]string, error) {
	var url string
	var headers map[string]string

	switch kind {
	case "anthropic":
		url = baseURL + "/v1/models"
		d.AuthHeader = "x-api-key"
		headers = map[string]string{"anthropic-version": "2023-06-01"}
	case "google":
		url = baseURL + "/v1beta/models"
		d.AuthHeader = "x-goog-api-key"
	default: // openai-compatible, custom
		url = baseURL + "/models"
	}

	var rawBody []byte
	var httpCode int
	var err error

	if headers != nil {
		rawBody, httpCode, err = rawGet(ctx, d, url, headers)
	} else {
		rawBody, httpCode, err = rawGet(ctx, d, url, nil)
	}
	if err != nil {
		return nil, err
	}
	if httpCode < 200 || httpCode >= 300 {
		return nil, fmt.Errorf("list models: HTTP %d", httpCode)
	}

	switch kind {
	case "google":
		return parseGoogleModels(rawBody)
	default:
		return parseOpenAIList(rawBody)
	}
}

// rawGet issues a GET and returns the body and status code.
func rawGet(ctx context.Context, d *check.Doer, url string, headers map[string]string) ([]byte, int, error) {
	req, err := newReq(ctx, url, d, headers)
	if err != nil {
		return nil, 0, err
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

func newReq(ctx context.Context, url string, d *check.Doer, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", check.UserAgent)
	if d.Key != "" {
		if d.AuthHeader != "" {
			req.Header.Set(d.AuthHeader, d.Key)
		} else {
			req.Header.Set("Authorization", "Bearer "+d.Key)
		}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// parseOpenAIList extracts data[].id from an OpenAI-style models response.
func parseOpenAIList(body []byte) ([]string, error) {
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}
	ids := make([]string, 0, len(v.Data))
	for _, m := range v.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// parseGoogleModels extracts models[].name stripped of "models/" prefix.
func parseGoogleModels(body []byte) ([]string, error) {
	var v struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}
	ids := make([]string, 0, len(v.Models))
	for _, m := range v.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// fetchOpenRouterCredits implements the openrouter-credits auto adapter.
func fetchOpenRouterCredits(ctx context.Context, d *check.Doer, baseURL string) (*UsageResult, error) {
	// baseURL is e.g. https://openrouter.ai/api/v1; credits live at /api/v1/credits.
	creditsURL := strings.TrimSuffix(baseURL, "/") + "/credits"
	body, code, err := rawGet(ctx, d, creditsURL, nil)
	if err != nil {
		return nil, err
	}
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("credits: HTTP %d", code)
	}
	var v struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("parse credits: %w", err)
	}
	remaining := v.Data.TotalCredits - v.Data.TotalUsage
	return &UsageResult{
		Value:     remaining,
		Unit:      "USD",
		Cap:       v.Data.TotalCredits,
		FetchedAt: time.Now(),
	}, nil
}
