package secret_test

import (
	"strings"
	"testing"

	"github.com/chriisbusy/status-slug/internal/secret"
)

func TestResolveEnv(t *testing.T) {
	t.Setenv("TEST_SSLUG_KEY", "sk-canary-abcdef123456")
	v, err := secret.Resolve("env:TEST_SSLUG_KEY")
	if err != nil {
		t.Fatalf("resolve env: %v", err)
	}
	if v != "sk-canary-abcdef123456" {
		t.Errorf("got %q", v)
	}
}

func TestResolveEnvMissing(t *testing.T) {
	_, err := secret.Resolve("env:DEFINITELY_NOT_SET_XYZ")
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestResolveNone(t *testing.T) {
	v, err := secret.Resolve("none")
	if err != nil || v != "" {
		t.Errorf("none: got %q err=%v", v, err)
	}
	v, err = secret.Resolve("")
	if err != nil || v != "" {
		t.Errorf("empty: got %q err=%v", v, err)
	}
}

func TestResolveFile(t *testing.T) {
	// Use a temp config dir so we don't touch the real store.
	t.Setenv("SSLUG_CONFIG_HOME", t.TempDir())
	canary := "sk-canary-file-9876543210"
	if err := secret.Store("file:testkey", canary); err != nil {
		t.Fatalf("store file: %v", err)
	}
	got, err := secret.Resolve("file:testkey")
	if err != nil {
		t.Fatalf("resolve file: %v", err)
	}
	if got != canary {
		t.Errorf("got %q want canary", got)
	}
}

func TestNegativeCanaryNotInErrors(t *testing.T) {
	canary := "sk-canary-abcdef1234567890"
	t.Setenv("CANARY_KEY", canary)

	// Trigger error paths with the canary present in env.
	errs := []error{}

	// Malformed ref containing canary.
	_, err := secret.Resolve("badscheme:" + canary)
	if err != nil {
		errs = append(errs, err)
	}

	// Unknown scheme.
	_, err = secret.Resolve("xyz:someid")
	if err != nil {
		errs = append(errs, err)
	}

	// Missing env var.
	_, err = secret.Resolve("env:NO_SUCH_VAR_XYZ")
	if err != nil {
		errs = append(errs, err)
	}

	for _, e := range errs {
		if e == nil {
			continue
		}
		if strings.Contains(e.Error(), canary) {
			t.Errorf("canary key material leaked in error: %v", e)
		}
	}
}

func TestRedact(t *testing.T) {
	cases := []struct{ in, wantPrefix string }{
		{"sk-abcdef123456", "sk"},
		{"ab", "****"},
		{"", "****"},
	}
	for _, tc := range cases {
		got := secret.Redact(tc.in)
		if tc.wantPrefix == "****" {
			if got != "****" {
				t.Errorf("Redact(%q): got %q", tc.in, got)
			}
		} else {
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("Redact(%q): got %q", tc.in, got)
			}
			if strings.Contains(got, tc.in[4:]) {
				t.Errorf("Redact(%q) leaked tail: %q", tc.in, got)
			}
		}
	}
}

func TestDeleteFile(t *testing.T) {
	t.Setenv("SSLUG_CONFIG_HOME", t.TempDir())
	if err := secret.Store("file:del-test", "value"); err != nil {
		t.Fatal(err)
	}
	if err := secret.Delete("file:del-test"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := secret.Resolve("file:del-test")
	if err == nil {
		t.Error("expected error after delete")
	}
	// Deleting again must not error.
	if err := secret.Delete("file:del-test"); err != nil {
		t.Errorf("double delete: %v", err)
	}
}
