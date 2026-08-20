package dashboard

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/state"
)

// Can the user type into the embedded wizard's name field?
func TestWizardTyping(t *testing.T) {
	m := New(config.Default(), state.New())
	m.width, m.height = 100, 40
	if m.wiz == nil {
		t.Fatal("wizard should auto-open on empty config")
	}
	// Run Init like tea.Program does, draining produced messages.
	cmds := m.Init()
	if cmds != nil {
		if msg := cmds(); msg != nil {
			tm, _ := m.Update(msg)
			m = tm.(model)
		}
	}
	// Type "abc" into the focused name field.
	for _, r := range "abc" {
		tm, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = tm.(model)
	}
	name := m.wiz.Info().Name
	t.Logf("name field after typing: %q", name)
	if name != "abc" {
		t.Fatalf("typing lost: name=%q want %q", name, "abc")
	}
}
