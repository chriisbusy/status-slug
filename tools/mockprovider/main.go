// mockprovider is a local HTTP server for e2e and manual TUI verification.
// Routes are namespaced by scenario: /ok, /billing, /auth, /down, /custom.
// Listen address: SSLUG_MOCK_ADDR (default :18821).
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("SSLUG_MOCK_ADDR")
	if addr == "" {
		addr = ":18821"
	}
	mux := http.NewServeMux()

	// /ok — healthy provider: models list + chat completions.
	mux.HandleFunc("/ok/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"object": "list",
			"data": []map[string]string{
				{"id": "mock-alpha", "object": "model"},
				{"id": "mock-beta", "object": "model"},
			},
		})
	})
	mux.HandleFunc("/ok/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "pong"}, "finish_reason": "length"}},
		})
	})

	// /billing — 402 with insufficient_quota body.
	mux.HandleFunc("/billing/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 402, map[string]any{
			"error": map[string]string{
				"message": "insufficient_quota: you have run out of credits",
				"type":    "billing_error",
			},
		})
	})

	// /auth — 401 unauthorized.
	mux.HandleFunc("/auth/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 401, map[string]any{
			"error": map[string]string{"message": "invalid api key", "type": "authentication_error"},
		})
	})

	// /down — hangs past client timeout.
	mux.HandleFunc("/down/v1/models", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Second)
		writeJSON(w, 200, map[string]any{"data": []string{}})
	})

	// /custom — arbitrary provider with openai-compatible endpoints.
	mux.HandleFunc("/custom/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"object": "list",
			"data": []map[string]string{
				{"id": "neuralwatt-large", "object": "model"},
				{"id": "neuralwatt-small", "object": "model"},
			},
		})
	})
	mux.HandleFunc("/custom/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"id":      "chatcmpl-nw",
			"object":  "chat.completion",
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "length"}},
		})
	})

	// /credits — OpenRouter-shaped credits endpoint.
	mux.HandleFunc("/api/v1/credits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"data": map[string]float64{"total_credits": 100.0, "total_usage": 21.8},
		})
	})

	log.Printf("mockprovider listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
