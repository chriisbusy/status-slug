// Package check runs probes and classifies their outcomes.
package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Status is the health classification of a probe result.
type Status string

const (
	OK      Status = "ok"
	Account Status = "account"
	Down    Status = "down"
	Unknown Status = "unknown"
)

// Result is a classified probe outcome.
type Result struct {
	Status    Status
	Reason    string
	HTTPCode  int
	LatencyMs float64
	CheckedAt time.Time
}

// Classify maps an HTTP response (or transport error) to a Result.
// body may be nil; err is non-nil for transport failures.
func Classify(httpCode int, body []byte, err error, latencyMs float64) Result {
	now := time.Now()
	r := Result{HTTPCode: httpCode, LatencyMs: latencyMs, CheckedAt: now}

	if err != nil {
		r.Status = Down
		r.Reason = classifyTransportErr(err)
		return r
	}

	// Check body for billing keywords regardless of status code.
	if containsBillingSignal(body) {
		r.Status = Account
		r.Reason = fmt.Sprintf("billing: %s", extractErrorMessage(body))
		return r
	}

	switch {
	case httpCode >= 200 && httpCode < 300:
		r.Status = OK
		r.Reason = "ok"
	case httpCode == 401 || httpCode == 403:
		r.Status = Account
		r.Reason = fmt.Sprintf("auth: HTTP %d", httpCode)
	case httpCode == 402:
		r.Status = Account
		r.Reason = "billing: HTTP 402"
	case httpCode == 404:
		r.Status = Down
		r.Reason = "probe endpoint 404 — check base_url"
	case httpCode == 429:
		r.Status = Account
		r.Reason = "quota: rate limited"
	case httpCode >= 500:
		r.Status = Down
		r.Reason = fmt.Sprintf("server error: HTTP %d", httpCode)
	default:
		r.Status = Down
		r.Reason = fmt.Sprintf("unexpected: HTTP %d", httpCode)
	}
	return r
}

// containsBillingSignal reports whether body carries a billing/quota marker.
func containsBillingSignal(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := strings.ToLower(string(body))
	for _, kw := range []string{"insufficient_quota", "billing", "credit_balance", "payment_required"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// extractErrorMessage pulls a human message from a JSON error body.
func extractErrorMessage(body []byte) string {
	var v struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &v) == nil {
		if v.Error.Message != "" {
			return v.Error.Message
		}
		if v.Message != "" {
			return v.Message
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// classifyTransportErr maps a network error to a reason string.
func classifyTransportErr(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns: " + dnsErr.Name
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return strings.ToLower(opErr.Op) + ": " + opErr.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	msg := err.Error()
	// Scrub anything that could be a URL-embedded key.
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// --- probe engine ---

const userAgent = "sslug/1.0"

// PoolSize is the maximum concurrent probes.
const PoolSize = 4

// Doer performs an HTTP request, injecting the bearer key and timeout.
type Doer struct {
	Client  *http.Client
	Timeout time.Duration
	Key     string // bearer token; may be empty
}

// NewDoer builds a Doer with the given timeout and optional key.
func NewDoer(timeout time.Duration, key string) *Doer {
	return &Doer{
		Client:  &http.Client{Timeout: timeout},
		Timeout: timeout,
		Key:     key,
	}
}

// Get issues a GET, classifying the result.
func (d *Doer) Get(ctx context.Context, url string) Result {
	return d.Do(ctx, http.MethodGet, url, nil, nil)
}

// Post issues a POST with a JSON body, classifying the result.
func (d *Doer) Post(ctx context.Context, url string, body []byte) Result {
	return d.Do(ctx, http.MethodPost, url, body, map[string]string{"Content-Type": "application/json"})
}

// Do performs the request with headers, classifying outcome.
func (d *Doer) Do(ctx context.Context, method, url string, body []byte, extraHeaders map[string]string) Result {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return Result{Status: Down, Reason: "bad request: " + err.Error(), CheckedAt: time.Now()}
	}
	req.Header.Set("User-Agent", userAgent)
	if d.Key != "" {
		req.Header.Set("Authorization", "Bearer "+d.Key)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := d.Client.Do(req)
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return Classify(0, nil, err, latency)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return Classify(resp.StatusCode, respBody, nil, latency)
}

// Job is one unit of work for the pool.
type Job struct {
	Provider string
	ModelID  string // empty = provider-level probe
	Run      func(ctx context.Context) Result
}

// Result carries the outcome of one Job.
type JobResult struct {
	Job    Job
	Result Result
}

// Run executes jobs with a bounded worker pool, sending progress on ch.
// ch is closed when all jobs complete. May be nil (fire and forget).
func Run(ctx context.Context, jobs []Job, ch chan<- JobResult) {
	sem := make(chan struct{}, PoolSize)
	done := make(chan struct{})
	go func() {
		for _, j := range jobs {
			j := j
			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()
				res := j.Run(ctx)
				if ch != nil {
					select {
					case ch <- JobResult{Job: j, Result: res}:
					case <-ctx.Done():
					}
				}
			}()
		}
		// Wait for all workers to drain.
		for i := 0; i < PoolSize; i++ {
			sem <- struct{}{}
		}
		close(done)
		if ch != nil {
			close(ch)
		}
	}()
	<-done
}
