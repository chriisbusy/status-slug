package dashboard

// harness_test.go drives the real program via tea.NewProgram with scripted
// input — the plan's prescribed dashboard-model test harness.

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/state"
)

// runProgram drives the dashboard with scripted input until the program
// exits, returning the final model.
func runProgram(t *testing.T, m model, input string) model {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	m.width, m.height = 120, 40
	var in bytes.Buffer
	in.WriteString(input)
	p := tea.NewProgram(m,
		tea.WithInput(&in),
		tea.WithOutput(io.Discard),
		tea.WithoutSignals(),
	)
	done := make(chan tea.Model, 1)
	go func() {
		fm, _ := p.Run()
		done <- fm
	}()
	select {
	case fm := <-done:
		if fm == nil {
			t.Fatal("program returned nil model")
		}
		return fm.(model)
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("program did not exit within 5s")
		return model{}
	}
}

func harnessModel(t *testing.T) model {
	t.Helper()
	cfg := config.Default()
	cfg.Settings.AutoRefresh = 0
	cfg.Settings.ConfirmQuit = false
	cfg.Providers = []config.Provider{
		{Name: "P1", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1/x", Enabled: true},
		{Name: "P2", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1/y", Enabled: true},
	}
	return New(cfg, state.New())
}

func TestHarness_QuitCleanly(t *testing.T) {
	m := runProgram(t, harnessModel(t), "q")
	if m.ov.kind != overlayNone {
		t.Error("overlay stuck open after quit")
	}
}

func TestHarness_TabCyclesFocus(t *testing.T) {
	// tab then q. Focus moves from status (0) to next visible panel.
	m := runProgram(t, harnessModel(t), "\tq")
	if m.focused == panelStatus {
		t.Error("tab did not move focus")
	}
}

func TestHarness_ZTogglesZoom(t *testing.T) {
	m := runProgram(t, harnessModel(t), "zq")
	if !m.zoomed {
		t.Error("z did not toggle zoom")
	}
}

func TestHarness_ViewCyclePersisted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SSLUG_STATE_HOME", tmp)
	m := harnessModel(t)
	m = runProgram(t, m, "pq")
	if m.st.UI.View != "compact" {
		t.Errorf("p should cycle full→compact, got %q", m.st.UI.View)
	}
	// State must persist to disk.
	st2, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st2.UI.View != "compact" {
		t.Errorf("view not persisted: %q", st2.UI.View)
	}
}

func TestHarness_MenusOpen(t *testing.T) {
	// Key opens the menu overlay; q closes the overlay; q quits.
	for _, k := range []string{"s", "u", "f", "t"} {
		m := runProgram(t, harnessModel(t), k+"qq")
		if m.ov.kind != overlayNone {
			t.Errorf("menu %q: overlay stuck", k)
		}
	}
}

func TestHarness_HelpOverlay(t *testing.T) {
	m := runProgram(t, harnessModel(t), "?qq")
	if m.ov.kind != overlayNone {
		t.Error("help overlay stuck")
	}
}

func TestHarness_EmptyConfigShowsNoProviders(t *testing.T) {
	cfg := config.Default()
	cfg.Settings.AutoRefresh = 0
	m := New(cfg, state.New())
	m.width, m.height = 120, 40
	frame := m.render()
	if !strings.Contains(frame, "no providers") {
		t.Error("empty config should render the no-providers hint")
	}
}

func TestHarness_NarrowRendersStack(t *testing.T) {
	m := harnessModel(t)
	m.width, m.height = 80, 40 // <100 forces stack
	frame := m.render()
	if !strings.Contains(frame, "[s]tatus") || !strings.Contains(frame, "[t]stats") {
		t.Error("stack render must include all panes")
	}
}
