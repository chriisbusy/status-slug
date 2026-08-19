package secret_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chriisbusy/status-slug/internal/secret"
)

func TestResolveEnv(t *testing.T) {
	t.Setenv("TEST_SSLUG_KEY", "CANARYabcdef123456789")
	v, err := secret.Resolve("env:TEST_SSLUG_KEY")
	if err != nil {
		t.Fatalf("resolve env: %v", err)
	}
	if v != "CANARYabcdef123456789" {
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
	canary := "CANARYfile9876543210"
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

// TestStoreFileMode0600: the file fallback must be 0600 (M03b).
func TestStoreFileMode0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SSLUG_CONFIG_HOME", dir)
	if err := secret.Store("file:modetest", "material"); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "secrets", "*.key"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 key file, got %v", matches)
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secret file mode: got %o want 600", perm)
	}
}

func TestNegativeCanaryNotInErrors(t *testing.T) {
	canary := "CANARYabcdef1234567897890"
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
