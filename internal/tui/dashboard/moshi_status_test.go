package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/chriisbusy/status-slug/internal/config"
	"testing"
)

func TestFormatMoshiStatusShowsDaemonAndHooks(t *testing.T) {
	status := moshiLocalStatus{
		Available: true,
		Daemon:    moshiDaemonProbe{Installed: true, Running: true, Gateway: true, Version: "0.3.13"},
		Pairing: moshiPairingStatus{
			Paired: true, HostID: "host_test", DisplayName: "workstation", SocketPath: "/run/user/1000/moshi-hook.sock",
			Hooks: []moshiHookStatus{
				{Target: "claude", Status: "stale", Missing: []string{"outdated"}, Command: "moshi-hook install --target claude"},
				{Target: "codex", Status: "installed"},
			},
		},
	}
	got := formatMoshiStatus(status)
	for _, want := range []string{"daemon", "running `true`", "gateway `true`", "paired `true`", "claude", "stale", "fix:", "codex", "installed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Moshi status missing %q:\n%s", want, got)
		}
	}
}

func TestMoshiStatusMessageUpdatesIntegrationsOverlay(t *testing.T) {
	m := newTestModel()
	m.ov = m.newIntegrationsOverlay()
	next, _ := m.Update(moshiStatusMsg{status: moshiLocalStatus{
		Available: true,
		Daemon:    moshiDaemonProbe{Installed: true, Running: true, Gateway: true, Version: "test"},
		Pairing:   moshiPairingStatus{Paired: true, Hooks: []moshiHookStatus{{Target: "omp", Status: "stale"}}},
	}})
	got := next.(model)
	content := got.ov.vp.View()
	for _, want := range []string{"Moshi daemon", "running", "omp", "stale", "moshi-hook pair", "moshi-hook service install", "moshi-hook install"} {
		if !strings.Contains(content, want) {
			t.Fatalf("integrations overlay missing %q:\n%s", want, content)
		}
	}
}

func TestMoshiDashboardLinesShowLiveStatesAndFreshness(t *testing.T) {
	m := newTestModel()
	m.moshiStatus = &moshiLocalStatus{
		Available: true,
		CheckedAt: time.Now(),
		Daemon:    moshiDaemonProbe{Installed: true, Running: true, Gateway: true, Version: "0.3.13"},
		Pairing: moshiPairingStatus{Paired: true, Hooks: []moshiHookStatus{
			{Target: "claude", Status: "stale"},
			{Target: "codex", Status: "installed"},
			{Target: "gemini", Status: "not_found"},
		}},
	}
	got := ansi.Strip(strings.Join(m.moshiDashboardLines(60), "\n"))
	for _, want := range []string{"Moshi daemon", "0.3.13", "Moshi claude hook", "stale", "Moshi codex hook", "installed", "just"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard status missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "gemini") {
		t.Fatalf("not-found hooks should stay in detailed view, not status pane:\n%s", got)
	}
}

func TestMoshiDashboardLinesDegradeHonestly(t *testing.T) {
	m := newTestModel()
	m.moshiStatus = &moshiLocalStatus{CheckedAt: time.Now(), Err: "moshi-hook not found"}
	got := ansi.Strip(strings.Join(m.moshiDashboardLines(60), "\n"))
	if !strings.Contains(got, "moshi-hook not found") {
		t.Fatalf("missing honest unavailable state: %s", got)
	}
}

func TestMoshiStatusRowsScrollWithProviders(t *testing.T) {
	m := newTestModel()
	for index := range 12 {
		m.cfg.Providers = append(m.cfg.Providers, config.Provider{Name: fmt.Sprintf("extra-%d", index), Enabled: true})
	}
	m.moshiStatus = &moshiLocalStatus{
		CheckedAt: time.Now(),
		Daemon:    moshiDaemonProbe{Installed: true, Running: true, Gateway: true, Version: "0.3.13"},
	}
	m.width, m.height = 60, 18
	m.scroll[panelStatus] = 1 << 20
	got := ansi.Strip(m.renderStatusPane(60, 8, false))
	if !strings.Contains(got, "Moshi daemon") {
		t.Fatalf("scrolling to bottom did not reveal Moshi status:\n%s", got)
	}
}

func TestOlderMoshiRefreshCannotReplaceNewerState(t *testing.T) {
	m := newTestModel()
	newer := time.Now()
	m.moshiStatus = &moshiLocalStatus{CheckedAt: newer, Daemon: moshiDaemonProbe{Version: "new"}}
	next, _ := m.Update(moshiStatusMsg{status: moshiLocalStatus{
		CheckedAt: newer.Add(-time.Minute), Daemon: moshiDaemonProbe{Version: "old"},
	}})
	got := next.(model)
	if got.moshiStatus.Daemon.Version != "new" {
		t.Fatalf("older refresh replaced newer state: %+v", got.moshiStatus)
	}
}

func TestMoshiRowsParticipateInStatusViewport(t *testing.T) {
	m := newTestModel()
	for index := range 12 {
		m.cfg.Providers = append(m.cfg.Providers, config.Provider{Name: fmt.Sprintf("extra-%d", index), Enabled: true})
	}
	m.width, m.height = 60, 18
	m.focused = panelStatus
	m.moshiStatus = &moshiLocalStatus{
		CheckedAt: time.Now(),
		Daemon:    moshiDaemonProbe{Running: true, Gateway: true, Version: "test"},
		Pairing:   moshiPairingStatus{Hooks: []moshiHookStatus{{Target: "omp", Status: "stale"}}},
	}
	visible := m.selectableViewportHeight(panelStatus)
	total := len(m.sortedProviders()) + len(m.moshiDashboardLines(m.paneContentWidthFor(panelStatus)))
	wantMax := max(0, total-visible)
	if got := m.maxScroll(panelStatus); got != wantMax {
		t.Fatalf("status maxScroll = %d, want %d including Moshi rows", got, wantMax)
	}
	for range len(m.sortedProviders()) {
		m.moveSelection(1)
	}
	if m.sel[panelStatus] >= m.scroll[panelStatus]+visible {
		t.Fatalf("selection %d escaped viewport [%d,%d)", m.sel[panelStatus], m.scroll[panelStatus], m.scroll[panelStatus]+visible)
	}
}

func TestMoshiStatusVisibleWithoutProviders(t *testing.T) {
	m := newTestModel()
	m.cfg.Providers = nil
	m.moshiStatus = &moshiLocalStatus{
		CheckedAt: time.Now(),
		Daemon:    moshiDaemonProbe{Running: true, Gateway: true, Version: "test"},
	}
	got := ansi.Strip(m.renderStatusPane(60, 8, false))
	if !strings.Contains(got, "Moshi daemon") {
		t.Fatalf("empty-provider status lost independent Moshi state:\n%s", got)
	}
}
