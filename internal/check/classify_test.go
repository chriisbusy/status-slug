package check_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chriisbusy/status-slug/internal/check"
)

func TestClassify_StatusCodes(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		body     string
		wantSt   check.Status
		wantPart string
	}{
		{"200 ok", 200, `{}`, check.OK, "ok"},
		{"201 ok", 201, `{}`, check.OK, "ok"},
		{"401 auth", 401, `{}`, check.Account, "auth: HTTP 401"},
		{"403 auth", 403, `{}`, check.Account, "auth: HTTP 403"},
		{"402 billing code", 402, `{}`, check.Account, "billing"},
		{"402 body insufficient_quota", 200, `{"error":{"message":"insufficient_quota exceeded"}}`, check.Account, "billing"},
		{"402 body billing keyword", 400, `billing required`, check.Account, "billing"},
		{"402 body credit_balance", 400, `credit_balance too low`, check.Account, "billing"},
		{"429 quota", 429, `{}`, check.Account, "quota: rate limited"},
		{"500 down", 500, `{}`, check.Down, "server error: HTTP 500"},
		{"503 down", 503, `{}`, check.Down, "server error: HTTP 503"},
		{"404 probe not found", 404, `{}`, check.Down, "404"},
		{"other 4xx down", 418, `{}`, check.Down, "unexpected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := check.Classify(tc.code, []byte(tc.body), nil, 1.0)
			if r.Status != tc.wantSt {
				t.Errorf("status: got %q want %q", r.Status, tc.wantSt)
			}
			if !strings.Contains(r.Reason, tc.wantPart) {
				t.Errorf("reason %q missing %q", r.Reason, tc.wantPart)
			}
		})
	}
}

func TestClassify_TransportErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"connection refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, "dial"},
		{"dns error", &net.DNSError{IsTimeout: true, Name: "api.example.com"}, "dns"},
		{"context deadline", context.DeadlineExceeded, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := check.Classify(0, nil, tc.err, 0)
			if r.Status != check.Down {
				t.Errorf("got %q want down", r.Status)
			}
			if !strings.Contains(r.Reason, tc.want) {
				t.Errorf("reason %q missing %q", r.Reason, tc.want)
			}
		})
	}
}

func TestClassify_NeverChecked(t *testing.T) {
	// Status unknown is set by callers, not Classify — verify constant exists.
	if check.Unknown != "unknown" {
		t.Fail()
	}
}

// TestClassify_URLErrorCanary: the P0 regression guard. A *url.Error carries
// the full request URL; for query-param-keyed providers that URL contains key
// material. Classify must never surface it in the reason.
func TestClassify_URLErrorCanary(t *testing.T) {
	canary := "CANARYleak111222333444555666999"
	urlErr := &url.Error{
		Op:  "Get",
		URL: "https://generativelanguage.googleapis.com/v1beta/models?key=" + canary,
		Err: errors.New("tls: failed to verify certificate"),
	}
	r := check.Classify(0, nil, urlErr, 0)
	if strings.Contains(r.Reason, canary) {
		t.Fatalf("KEY LEAK: canary in reason: %q", r.Reason)
	}
	if strings.Contains(r.Reason, "key=") {
		t.Errorf("URL query leaked into reason: %q", r.Reason)
	}
	if r.Status != check.Down {
		t.Errorf("status: got %q want down", r.Status)
	}
	// The underlying cause must survive unwrapping.
	if !strings.Contains(r.Reason, "tls") {
		t.Errorf("inner cause lost: %q", r.Reason)
	}
}

// TestClassify_404ReasonIsSpecific: 404 on the probe URL must say so, not
// fall through to the generic "unexpected" branch.
func TestClassify_404ReasonIsSpecific(t *testing.T) {
	r := check.Classify(404, []byte(`{}`), nil, 1)
	if r.Status != check.Down {
		t.Fatalf("status: got %q", r.Status)
	}
	if !strings.Contains(r.Reason, "probe endpoint") {
		t.Errorf("reason %q should name the probe endpoint", r.Reason)
	}
}

func TestDoer_Integration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(200)
			fmt.Fprint(w, `{"data":[]}`)
		case "/billing":
			w.WriteHeader(402)
			fmt.Fprint(w, `{"error":{"message":"insufficient_quota"}}`)
		case "/auth":
			w.WriteHeader(401)
		case "/slow":
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	d := check.NewDoer(5*time.Second, "test-key")

	t.Run("ok endpoint", func(t *testing.T) {
		r := d.Get(context.Background(), srv.URL+"/ok")
		if r.Status != check.OK {
			t.Errorf("got %q reason=%q", r.Status, r.Reason)
		}
		if r.HTTPCode != 200 {
			t.Errorf("http_code: got %d", r.HTTPCode)
		}
	})

	t.Run("billing endpoint", func(t *testing.T) {
		r := d.Get(context.Background(), srv.URL+"/billing")
		if r.Status != check.Account {
			t.Errorf("got %q", r.Status)
		}
	})

	t.Run("auth endpoint", func(t *testing.T) {
		r := d.Get(context.Background(), srv.URL+"/auth")
		if r.Status != check.Account || !strings.Contains(r.Reason, "401") {
			t.Errorf("got %q %q", r.Status, r.Reason)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		short := check.NewDoer(50*time.Millisecond, "")
		r := short.Get(context.Background(), srv.URL+"/slow")
		if r.Status != check.Down {
			t.Errorf("got %q want down", r.Status)
		}
		if !strings.Contains(r.Reason, "timeout") && !strings.Contains(r.Reason, "deadline") {
			t.Errorf("reason %q should mention timeout", r.Reason)
		}
	})

	t.Run("dial refused", func(t *testing.T) {
		// Port 1 is guaranteed closed.
		r := d.Get(context.Background(), "http://127.0.0.1:1/x")
		if r.Status != check.Down {
			t.Errorf("got %q want down", r.Status)
		}
	})

	t.Run("bearer header sent", func(t *testing.T) {
		var gotAuth string
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
		}))
		defer srv2.Close()
		d.Get(context.Background(), srv2.URL)
		if gotAuth != "Bearer test-key" {
			t.Errorf("Authorization: got %q", gotAuth)
		}
	})
}

func TestRun_PoolConcurrency(t *testing.T) {
	var peak int64
	var current int64
	jobs := make([]check.Job, 12)
	for i := range jobs {
		jobs[i] = check.Job{
			Provider: "p",
			Run: func(ctx context.Context) check.Result {
				n := atomic.AddInt64(&current, 1)
				defer atomic.AddInt64(&current, -1)
				for {
					p := atomic.LoadInt64(&peak)
					if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				return check.Result{Status: check.OK}
			},
		}
	}
	ch := make(chan check.JobResult, len(jobs))
	check.Run(context.Background(), jobs, ch)
	var count int
	for range ch {
		count++
	}
	if count != len(jobs) {
		t.Errorf("got %d results want %d", count, len(jobs))
	}
	if atomic.LoadInt64(&peak) > check.PoolSize {
		t.Errorf("peak concurrency %d exceeds pool size %d", peak, check.PoolSize)
	}
}
