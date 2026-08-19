package state_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chriisbusy/status-slug/internal/state"
)

func TestRingCapsAtHistoryLength(t *testing.T) {
	f := state.New()
	for i := 0; i < 100; i++ {
		f.RecordCheck("p", state.CheckResult{
			Status: "ok", LatencyMs: float64(i), CheckedAt: time.Now(),
		}, 20)
	}
	ring := f.Providers["p"].Ring
	if len(ring) != 20 {
		t.Errorf("ring len: got %d want 20", len(ring))
	}
	// Oldest should be evicted: first element should be 80 (100-20).
	if ring[0] != 80 {
		t.Errorf("ring[0]: got %v want 80", ring[0])
	}
}

func TestCorruptStateRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := state.LoadFrom(path)
	if err != nil {
		t.Fatalf("expected no error on corrupt state, got %v", err)
	}
	if f == nil {
		t.Fatal("expected fresh state")
	}
	// .bak must exist.
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("expected .bak file: %v", err)
	}
}

func TestMeterRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	f := state.New()
	f.SetMeter("Neuralwatt", "Energy", 231.5)
	if err := f.SaveTo(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := state.LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mv := got.GetMeter("Neuralwatt", "Energy")
	if mv == nil {
		t.Fatal("meter not found after roundtrip")
	}
	if mv.Value != 231.5 {
		t.Errorf("value: got %v want 231.5", mv.Value)
	}
	if mv.SetAt.IsZero() {
		t.Error("set_at is zero")
	}
}

func TestRecordModelCheck(t *testing.T) {
	f := state.New()
	now := time.Now()
	f.RecordModelCheck("OpenAI", "gpt-5-mini", state.CheckResult{
		Status: "ok", LatencyMs: 98, CheckedAt: now,
	}, 60)
	f.RecordModelCheck("OpenAI", "gpt-5-mini", state.CheckResult{
		Status: "down", LatencyMs: 5000, CheckedAt: now,
	}, 60)

	m := f.Model("OpenAI", "gpt-5-mini")
	if m.Counters.Checks != 2 || m.Counters.OK != 1 || m.Counters.Down != 1 {
		t.Errorf("counters: %+v", m.Counters)
	}
	if len(m.Ring) != 2 {
		t.Errorf("ring: got %d entries", len(m.Ring))
	}
}

func TestSaveToMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	f := state.New()
	if err := f.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode: got %o want 600", perm)
	}
}

func TestStatePathOverride(t *testing.T) {
	t.Setenv("SSLUG_STATE_HOME", "/tmp/sslug-state-test")
	got := state.Path()
	if got != "/tmp/sslug-state-test/state.json" {
		t.Errorf("Path: got %q", got)
	}
}

// TestRelAgeBoundaries: seconds-precision must say "just now" (M20).
func TestRelAgeBoundaries(t *testing.T) {
	if got := state.RelAge(30 * time.Second); got != "just now" {
		t.Errorf("30s: got %q want 'just now'", got)
	}
	if got := state.RelAge(59 * time.Second); got != "just now" {
		t.Errorf("59s: got %q want 'just now'", got)
	}
	if got := state.RelAge(61 * time.Second); got != "1m ago" {
		t.Errorf("61s: got %q want '1m ago'", got)
	}
}

func TestJSONShape(t *testing.T) {
	// Verify the serialized shape includes the expected top-level keys.
	f := state.New()
	f.SetMeter("P", "M", 1.0)
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(data, &v)
	for _, k := range []string{"ui", "providers", "meters"} {
		if _, ok := v[k]; !ok {
			t.Errorf("missing top-level key %q in state JSON", k)
		}
	}
}
