package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/theme"
)

type moshiHookStatus struct {
	Target  string   `json:"target"`
	Status  string   `json:"status"`
	Missing []string `json:"missing"`
	Command string   `json:"command"`
}

type moshiPairingStatus struct {
	Paired      bool              `json:"paired"`
	HostID      string            `json:"hostId"`
	DisplayName string            `json:"displayName"`
	BaseURL     string            `json:"baseUrl"`
	SocketPath  string            `json:"socketPath"`
	Hooks       []moshiHookStatus `json:"hooks"`
}

type moshiDaemonProbe struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Gateway   bool   `json:"gateway"`
	Version   string `json:"version"`
	HostID    string `json:"hostId"`
}

type moshiLocalStatus struct {
	Available bool
	CheckedAt time.Time
	Recent    []string
	Pairing   moshiPairingStatus
	Daemon    moshiDaemonProbe
	Err       string
}

type moshiStatusMsg struct{ status moshiLocalStatus }
type moshiRepairMsg struct{ err string }

func moshiStatusCmd() tea.Cmd {
	return func() tea.Msg {
		checkedAt := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		path, err := exec.LookPath("moshi-hook")
		if err != nil {
			return moshiStatusMsg{status: moshiLocalStatus{CheckedAt: checkedAt, Err: "moshi-hook not found"}}
		}
		statusJSON, err := exec.CommandContext(ctx, path, "status", "--json").Output()
		if err != nil {
			return moshiStatusMsg{status: moshiLocalStatus{Available: true, CheckedAt: checkedAt, Err: "moshi-hook status failed"}}
		}
		probeJSON, err := exec.CommandContext(ctx, path, "probe", "--json").Output()
		if err != nil {
			return moshiStatusMsg{status: moshiLocalStatus{Available: true, CheckedAt: checkedAt, Err: "moshi-hook probe failed"}}
		}
		status := moshiLocalStatus{Available: true, CheckedAt: checkedAt}
		if err := json.Unmarshal(statusJSON, &status.Pairing); err != nil {
			status.Err = "invalid moshi-hook status response"
			return moshiStatusMsg{status: status}
		}
		if err := json.Unmarshal(probeJSON, &status.Daemon); err != nil {
			status.Err = "invalid moshi-hook probe response"
			return moshiStatusMsg{status: status}
		}
		if logOutput, err := exec.CommandContext(ctx, path, "logs", "-n", "8").Output(); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(logOutput)), "\n") {
				if line != "" && !strings.HasPrefix(line, "# ") {
					status.Recent = append(status.Recent, line)
				}
			}
		}
		return moshiStatusMsg{status: status}
	}
}

func moshiRepairCmd(targets []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		path, err := exec.LookPath("moshi-hook")
		if err != nil {
			return moshiRepairMsg{err: "moshi-hook not found"}
		}
		if err := exec.CommandContext(ctx, path, "install", "--target", strings.Join(targets, ",")).Run(); err != nil {
			return moshiRepairMsg{err: "moshi-hook install failed"}
		}
		return moshiRepairMsg{}
	}
}

func formatMoshiStatus(status moshiLocalStatus) string {
	if status.Err != "" {
		return "## Moshi daemon and hooks\n\n- status: **unavailable** — " + status.Err
	}
	lines := []string{
		"## Moshi daemon and hooks", "",
		fmt.Sprintf("- daemon: installed `%t` · running `%t` · gateway `%t` · version `%s`", status.Daemon.Installed, status.Daemon.Running, status.Daemon.Gateway, status.Daemon.Version),
		fmt.Sprintf("- pairing: paired `%t` · host `%s` · display `%s`", status.Pairing.Paired, status.Pairing.HostID, status.Pairing.DisplayName),
		fmt.Sprintf("- socket: `%s`", status.Pairing.SocketPath), "- hooks:",
	}
	for _, hook := range status.Pairing.Hooks {
		line := fmt.Sprintf("  - `%s`: **%s**", hook.Target, strings.ReplaceAll(hook.Status, "_", " "))
		if len(hook.Missing) > 0 {
			line += fmt.Sprintf(" · %d missing/outdated", len(hook.Missing))
		}
		if hook.Command != "" && hook.Status != "installed" {
			line += fmt.Sprintf(" · fix: `%s`", hook.Command)
		}
		lines = append(lines, line)
	}
	if len(status.Recent) > 0 {
		lines = append(lines, "", "### Recent daemon activity", "", "```text")
		lines = append(lines, status.Recent...)
		lines = append(lines, "```")
	}
	return strings.Join(lines, "\n")
}

func (m model) moshiDashboardLines(width int) []string {
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Muted]))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette[theme.Title])).Bold(true)
	if m.moshiStatus == nil {
		return nil
	}
	status := m.moshiStatus
	age := ""
	if !status.CheckedAt.IsZero() {
		age = state.RelAge(time.Since(status.CheckedAt))
	}
	if status.Err != "" {
		return []string{fitCells(m.glyphFor("down")+" "+title.Render("Moshi")+" "+muted.Render(status.Err+" · "+age), width)}
	}
	daemonState := "down"
	if status.Daemon.Running && status.Daemon.Gateway {
		daemonState = "ok"
	} else if status.Daemon.Running || status.Pairing.Paired {
		daemonState = "account"
	}
	lines := []string{fitCells(m.glyphFor(daemonState)+" "+title.Render("Moshi daemon")+" "+muted.Render("v"+status.Daemon.Version+" · "+displayAge(age)), width)}
	stale, broken, current := 0, 0, 0
	for _, hook := range status.Pairing.Hooks {
		switch hook.Status {
		case "installed", "current":
			current++
		case "stale":
			stale++
		case "broken":
			broken++
		}
	}
	hookState := "ok"
	if broken > 0 {
		hookState = "down"
	} else if stale > 0 {
		hookState = "account"
	}
	lines = append(lines, fitCells(
		m.glyphFor(hookState)+" "+title.Render("Moshi hooks")+" "+
			muted.Render(fmt.Sprintf("%d current · %d stale · %d broken · g details", current, stale, broken)), width))
	return lines
}

func (m model) staleMoshiTargets() []string {
	if m.moshiStatus == nil {
		return nil
	}
	var targets []string
	for _, hook := range m.moshiStatus.Pairing.Hooks {
		if hook.Status == "stale" || hook.Status == "broken" {
			targets = append(targets, hook.Target)
		}
	}
	return targets
}
