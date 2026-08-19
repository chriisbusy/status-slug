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
type Adapter interface {
	// Probe checks provider-level health.
	Probe(ctx context.Context, doer *check.Doer, baseURL string) check.Result
	// ProbeModel checks one model with a minimal chat completion.
	ProbeModel(ctx context.Context, doer *check.Doer, baseURL, modelID string) check.Result
	// ListModels returns available model IDs.
	ListModels(ctx context.Context, doer *check.Doer, baseURL string) ([]string, error)
	// FetchUsage fetches usage data for auto meters. Returns nil if unsupported.
	FetchUsage(ctx context.Context, doer *check.Doer, baseURL, autoID string) (*UsageResult, error)
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

func (a openaiAdapter) Probe(ctx context.Context, d *check.Doer, baseURL string) check.Result {
	return d.Get(ctx, baseURL+"/models")
}

func (a openaiAdapter) ProbeModel(ctx context.Context, d *check.Doer, baseURL, modelID string) check.Result {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`, modelID)
	return d.Post(ctx, baseURL+"/chat/completions", []byte(body))
}

func (a openaiAdapter) ListModels(ctx context.Context, d *check.Doer, baseURL string) ([]string, error) {
	return ListModelsRaw(ctx, d, "openai-compatible", baseURL)
}

func (a openaiAdapter) FetchUsage(ctx context.Context, d *check.Doer, baseURL, autoID string) (*UsageResult, error) {
	if autoID == "openrouter-credits" {
		return fetchOpenRouterCredits(ctx, d, baseURL)
	}
	return nil, fmt.Errorf("unknown auto usage adapter %q", autoID)
}

// --- anthropic ---

type anthropicAdapter struct{}

func (a anthropicAdapter) Probe(ctx context.Context, d *check.Doer, baseURL string) check.Result {
	return d.Do(ctx, "GET", baseURL+"/v1/models", nil, map[string]string{
		"x-api-key":         d.Key,
		"anthropic-version": "2023-06-01",
	})
}

func (a anthropicAdapter) ProbeModel(ctx context.Context, d *check.Doer, baseURL, modelID string) check.Result {
	body := fmt.Sprintf(`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`, modelID)
	return d.Do(ctx, "POST", baseURL+"/v1/messages", []byte(body), map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         d.Key,
		"anthropic-version": "2023-06-01",
	})
}

func (a anthropicAdapter) ListModels(ctx context.Context, d *check.Doer, baseURL string) ([]string, error) {
	return ListModelsRaw(ctx, d, "anthropic", baseURL)
}

func (a anthropicAdapter) FetchUsage(_ context.Context, _ *check.Doer, _, _ string) (*UsageResult, error) {
	return nil, nil
}

// --- google ---

type googleAdapter struct{}

func (a googleAdapter) Probe(ctx context.Context, d *check.Doer, baseURL string) check.Result {
	url := baseURL + "/v1beta/models"
	if d.Key != "" {
		url += "?key=" + d.Key
	}
	return d.Get(ctx, url)
}

func (a googleAdapter) ProbeModel(ctx context.Context, d *check.Doer, baseURL, modelID string) check.Result {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", baseURL, modelID)
	if d.Key != "" {
		url += "?key=" + d.Key
	}
	body := `{"contents":[{"parts":[{"text":"ping"}]}],"generationConfig":{"maxOutputTokens":1}}`
	return d.Post(ctx, url, []byte(body))
}

func (a googleAdapter) ListModels(ctx context.Context, d *check.Doer, baseURL string) ([]string, error) {
	return ListModelsRaw(ctx, d, "google", baseURL)
}

func (a googleAdapter) FetchUsage(_ context.Context, _ *check.Doer, _, _ string) (*UsageResult, error) {
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
		headers = map[string]string{"x-api-key": d.Key, "anthropic-version": "2023-06-01"}
	case "google":
		url = baseURL + "/v1beta/models"
		if d.Key != "" {
			url += "?key=" + d.Key
		}
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
	req, err := newReq(ctx, url, d.Key, headers)
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

func newReq(ctx context.Context, url, key string, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sslug/1.0")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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
