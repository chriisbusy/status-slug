package dashboard

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Supervisor check: render every pane at hostile widths with multibyte
// content; output must always be valid UTF-8.
func TestUTF8NoCorruption(t *testing.T) {
	m := newTestModel()
	m.cfg.Providers[0].Name = "プロバイダー日本語"
	for w := 1; w <= 65; w++ {
		frame := m.renderPane(panelStatus, w, 6)
		if !utf8.ValidString(frame) {
			t.Fatalf("width %d: invalid UTF-8 in pane render", w)
		}
	}
	// truncate() directly on a multibyte reason.
	r := truncate("probe endpoint 404 — check base_url", 4)
	if !utf8.ValidString(r) {
		t.Fatalf("truncate produced invalid UTF-8: %q", r)
	}
	if !strings.ContainsRune("probe endpoint 404 — check base_url", '—') {
		t.Fatal("test string lost its em-dash")
	}
}
