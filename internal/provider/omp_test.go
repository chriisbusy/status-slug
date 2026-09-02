package provider

import "testing"

func TestParseOMPUsageWindow(t *testing.T) {
	data := []byte(`{"reports":[{"provider":"openai-codex","limits":[{"scope":{"provider":"openai-codex","windowId":"7d"},"window":{"resetsAt":1789},"amount":{"used":73,"limit":100,"usedFraction":0.73,"unit":"percent"}}]}]}`)
	got, err := parseOMPUsage(data, "openai-codex", "7d")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != 73 || got.Cap != 100 || got.Unit != "percent" {
		t.Fatalf("usage = %+v", got)
	}
}
