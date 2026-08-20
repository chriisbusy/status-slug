// sslug — btop-style LLM provider status TUI and CLI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/charmbracelet/x/term"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/serve"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/tui/dashboard"
)

var version = "dev"

// setupLog wires --verbose to a styled file log at the state dir.
// Keys are never logged (CONSTITUTION invariant 1).
// Returns the remaining args with the flag stripped.
func setupLog(args []string) []string {
	verbose := false
	out := args[:0]
	for _, a := range args {
		if a == "--verbose" || a == "-v" {
			verbose = true
		} else {
			out = append(out, a)
		}
	}
	if !verbose {
		return out
	}
	logPath := filepath.Join(filepath.Dir(state.Path()), "sslug.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sslug: cannot open log file: %v\n", err)
		return out
	}
	log.SetOutput(f)
	log.SetLevel(log.DebugLevel)
	log.SetReportTimestamp(true)
	log.Debug("sslug starting", "version", versionString(), "pid", os.Getpid())
	return out
}

// versionString returns the ldflags-injected version, or the Go module
// version embedded by `go install ...@latest`, or "dev".
func versionString() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	os.Args = append(os.Args[:1], setupLog(os.Args[1:])...)
	check.UserAgent = "sslug/" + versionString()
	if len(os.Args) < 2 {
		runDashboard(os.Args[1:])
		return
	}
	switch os.Args[1] {
	case "setup":
		runSetup(os.Args[2:])
	case "check":
		runCheck(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "usage":
		runUsage(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "providers":
		runProviders(os.Args[2:])
	case "remove":
		runRemove(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "config":
		runConfig(os.Args[2:])
	case "version":
		fmt.Println("sslug", versionString())
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "sslug: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`sslug — LLM provider status board

Usage:
  sslug                    dashboard TUI (wizard on first run)
  sslug setup [name]       add or reconfigure a provider
  sslug check [flags]      probe all providers + favourites
  sslug status [flags]     print last snapshot (no network)
  sslug usage [flags]      print usage/meter snapshot (no network)
  sslug usage set P M V    record a manual meter value
  sslug serve [flags]      loopback HTTP status/usage server
  sslug providers          list configured providers
  sslug remove <name>      delete provider and its secrets
  sslug doctor             diagnostics
  sslug config path        print config file location
  sslug version            print version`)
}

// --- helpers ---

func mustLoadConfig() config.Config {
	cfg, err := config.Load()
	if err != nil {
		fatal("load config", err)
	}
	return cfg
}

func mustLoadState() *state.File {
	st, err := state.Load()
	if err != nil {
		fatal("load state", err)
	}
	return st
}

func fatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "sslug: %s: %v\n", what, err)
	os.Exit(1)
}

// resolveKey resolves a provider's key_ref, never logging the result.
func resolveKey(p config.Provider) string {
	key, err := secret.Resolve(p.KeyRef)
	if err != nil {
		// Do not include the error's detail if it might contain material.
		fmt.Fprintf(os.Stderr, "sslug: warning: key for %q unavailable\n", p.Name)
		return ""
	}
	return key
}

func runDashboard(_ []string) {
	// Dashboard handles first-run wizard popup itself (empty config).
	if !term.IsTerminal(os.Stdout.Fd()) {
		fmt.Fprintln(os.Stderr, "sslug: the dashboard needs a terminal — try `sslug status` or `sslug check --json` for scripted use")
		os.Exit(1)
	}
	if err := dashboard.Run(); err != nil {
		fatal("dashboard", err)
	}
}

func runSetup(args []string) {
	if !term.IsTerminal(os.Stdout.Fd()) {
		fmt.Fprintln(os.Stderr, "sslug: the setup wizard needs a terminal — edit `sslug config path` directly for scripted provisioning")
		os.Exit(1)
	}
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	cfg := mustLoadConfig()
	st := mustLoadState()
	if err := dashboard.RunWizard(cfg, st, name); err != nil {
		fatal("setup", err)
	}
}

// --- sslug check ---

func runCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	providerFlag := fs.String("provider", "", "check only this provider")
	jsonFlag := fs.Bool("json", false, "output JSON")
	strictFlag := fs.Bool("strict", false, "exit 3 if any non-green")
	fs.Parse(args)

	cfg := mustLoadConfig()
	st := mustLoadState()

	timeout := time.Duration(cfg.Settings.ProbeTimeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	jobs := provider.BuildJobs(cfg, resolveKey, timeout, *providerFlag)

	if len(jobs) == 0 {
		if *jsonFlag {
			fmt.Println(`{"schema":1,"results":[]}`)
		} else {
			fmt.Println("no providers configured — run: sslug setup")
		}
		return
	}

	ctx := context.Background()

	ch := make(chan check.JobResult, len(jobs))
	go check.Run(ctx, jobs, ch)

	type jsonResult struct {
		Provider  string  `json:"provider"`
		Model     string  `json:"model,omitempty"`
		Status    string  `json:"status"`
		Reason    string  `json:"reason,omitempty"`
		HTTPCode  int     `json:"http_code,omitempty"`
		LatencyMs float64 `json:"latency_ms,omitempty"`
		CheckedAt string  `json:"checked_at"`
	}
	var results []jsonResult
	var anyNonGreen bool

	for jr := range ch {
		res := jr.Result
		log.Debug("probe result", "provider", jr.Job.Provider, "model", jr.Job.ModelID,
			"status", res.Status, "http", res.HTTPCode, "latency_ms", res.LatencyMs)
		scr := state.CheckResult{
			Status:    string(res.Status),
			Reason:    res.Reason,
			HTTPCode:  res.HTTPCode,
			LatencyMs: res.LatencyMs,
			CheckedAt: res.CheckedAt,
		}
		if jr.Job.ModelID == "" {
			st.RecordCheck(jr.Job.Provider, scr, cfg.Settings.HistoryLength)
		} else {
			st.RecordModelCheck(jr.Job.Provider, jr.Job.ModelID, scr, cfg.Settings.HistoryLength)
		}
		if res.Status != check.OK {
			anyNonGreen = true
		}
		results = append(results, jsonResult{
			Provider:  jr.Job.Provider,
			Model:     jr.Job.ModelID,
			Status:    string(res.Status),
			Reason:    res.Reason,
			HTTPCode:  res.HTTPCode,
			LatencyMs: res.LatencyMs,
			CheckedAt: res.CheckedAt.UTC().Format(time.RFC3339),
		})
	}

	for _, u := range provider.RefreshAutoMeters(ctx, cfg, timeout, *providerFlag, resolveKey) {
		st.SetMeter(u.Provider, u.Meter, u.Value)
	}

	if err := st.Save(); err != nil {
		fatal("save state", err)
	}

	if *jsonFlag {
		out := map[string]any{"schema": 1, "results": results}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	} else {
		for _, r := range results {
			label := r.Provider
			if r.Model != "" {
				label += "/" + r.Model
			}
			marker := map[string]string{"ok": "●", "account": "◐", "down": "○"}[r.Status]
			if marker == "" {
				marker = "◌"
			}
			lat := ""
			if r.LatencyMs > 0 {
				lat = fmt.Sprintf("  %.0fms", r.LatencyMs)
			}
			fmt.Printf("%s %-30s  %-8s%s  %s\n", marker, label, r.Status, lat, r.Reason)
		}
	}

	if *strictFlag && anyNonGreen {
		os.Exit(3)
	}
}

// --- sslug status ---

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	format := fs.String("format", "plain", "plain|tmux|json")
	fs.Parse(args)

	cfg := mustLoadConfig()
	st := mustLoadState()

	snap := serve.BuildSnapshot(cfg, st)

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(snap)
	case "tmux":
		ok, account, down := 0, 0, 0
		for _, p := range snap.Providers {
			switch p.Status {
			case "ok":
				ok++
			case "account":
				account++
			case "down":
				down++
			}
		}
		fmt.Printf("●%d ◐%d ○%d\n", ok, account, down)
	default: // plain
		for _, p := range snap.Providers {
			marker := map[string]string{"ok": "●", "account": "◐", "down": "○"}[p.Status]
			if marker == "" {
				marker = "◌"
			}
			age := ""
			if !p.CheckedAt.IsZero() {
				age = state.RelAge(time.Since(p.CheckedAt))
			}
			fmt.Printf("%s %-20s  %-8s  %s\n", marker, p.Name, p.Status, age)
		}
	}
}

// --- sslug usage ---

func runUsage(args []string) {
	if len(args) > 0 && args[0] == "set" {
		runUsageSet(args[1:])
		return
	}
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	format := fs.String("format", "plain", "plain|json|moshi")
	fs.Parse(args)

	cfg := mustLoadConfig()
	st := mustLoadState()

	switch *format {
	case "moshi":
		snaps := provider.MoshiBuild(cfg, st, time.Now())
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(snaps)
	case "json":
		snap := serve.BuildSnapshot(cfg, st)
		type usageOut struct {
			Schema int                            `json:"schema"`
			Meters map[string]serve.SnapshotMeter `json:"meters"`
		}
		out := usageOut{Schema: 1, Meters: map[string]serve.SnapshotMeter{}}
		for _, p := range snap.Providers {
			for _, m := range p.Meters {
				out.Meters[p.Name+"/"+m.Name] = m
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	default: // plain
		snap := serve.BuildSnapshot(cfg, st)
		for _, p := range snap.Providers {
			if len(p.Meters) == 0 {
				continue
			}
			fmt.Println(p.Name)
			for _, m := range p.Meters {
				cap := ""
				if m.Cap > 0 {
					cap = fmt.Sprintf("/%.4g", m.Cap)
				}
				age := ""
				if !m.SetAt.IsZero() {
					age = "  (" + state.RelAge(time.Since(m.SetAt)) + ")"
				}
				fmt.Printf("  %-20s %.4g%s %s%s\n", m.Name, m.Value, cap, m.Unit, age)
			}
		}
	}
}

// --- sslug usage set ---

func runUsageSet(args []string) {
	fs := flag.NewFlagSet("usage set", flag.ExitOnError)
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sslug usage set <provider> <meter> <value>")
		os.Exit(1)
	}
	provName, meterName, valStr := rest[0], rest[1], rest[2]

	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		fatal("parse value", err)
	}

	cfg := mustLoadConfig()
	p := cfg.Find(provName)
	if p == nil {
		fmt.Fprintf(os.Stderr, "sslug: provider %q not found\n", provName)
		os.Exit(1)
	}
	var found bool
	var available []string
	for _, m := range p.Meters {
		available = append(available, m.Name)
		if m.Name == meterName {
			found = true
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "sslug: meter %q not defined for %q\navailable meters: %s\n",
			meterName, provName, strings.Join(available, ", "))
		os.Exit(1)
	}

	st := mustLoadState()
	st.SetMeter(provName, meterName, val)
	if err := st.Save(); err != nil {
		fatal("save state", err)
	}
	fmt.Printf("%s/%s = %.4g\n", provName, meterName, val)
}

// --- sslug serve ---

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "", "listen address (loopback only)")
	fs.Parse(args)

	cfg := mustLoadConfig()
	addr := "127.0.0.1:19777"
	if cfg.Settings.ServeListen != "" {
		addr = cfg.Settings.ServeListen
	}
	// Explicit flag wins and is persisted (plan: "--listen and config-saved").
	flagSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			flagSet = true
		}
	})
	if flagSet {
		addr = *listen
		if cfg.Settings.ServeListen != addr {
			cfg.Settings.ServeListen = addr
			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "sslug: warning: could not persist listen addr: %v\n", err)
			}
		}
	}

	mux := serve.NewMux()
	fmt.Printf("sslug serve listening on http://%s\n", addr)
	if err := serve.ListenAndServe(addr, mux); err != nil {
		fatal("serve", err)
	}
}

// --- sslug providers ---

func runProviders(_ []string) {
	cfg := mustLoadConfig()
	if len(cfg.Providers) == 0 {
		fmt.Println("no providers configured")
		return
	}
	for _, p := range cfg.Providers {
		status := "enabled"
		if !p.Enabled {
			status = "disabled"
		}
		fmt.Printf("%-24s  %-20s  %s  %s\n", p.Name, p.Kind, p.BaseURL, status)
	}
}

// --- sslug remove ---

func runRemove(args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sslug remove <name>")
		os.Exit(1)
	}
	name := fs.Arg(0)
	cfg := mustLoadConfig()
	p := cfg.Find(name)
	if p == nil {
		fmt.Fprintf(os.Stderr, "sslug: provider %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Remove provider %q and its stored key? [y/N] ", name)
	var ans string
	fmt.Scanln(&ans)
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		fmt.Println("aborted")
		return
	}
	if err := secret.Delete(p.KeyRef); err != nil {
		fmt.Fprintf(os.Stderr, "sslug: warning: key delete: %v\n", err)
	}
	cfg.Remove(name)
	if err := config.Save(cfg); err != nil {
		fatal("save config", err)
	}
	fmt.Printf("removed %q\n", name)
}

// --- sslug doctor ---

func runDoctor(_ []string) {
	var failed bool

	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("config:    FAIL  %v\n", err)
		failed = true
	} else {
		fmt.Printf("config:    OK    %s (%d providers)\n", config.Path(), len(cfg.Providers))
	}

	if secret.KeyringAvailable() {
		fmt.Println("keyring:   OK    OS keyring reachable")
	} else {
		fmt.Println("keyring:   WARN  not available (headless?); file/env fallback active")
	}

	for _, p := range cfg.Providers {
		key, err := secret.Resolve(p.KeyRef)
		switch {
		case err != nil:
			fmt.Printf("key %-16s FAIL  %s\n", p.Name+":", secret.Redact(p.KeyRef))
			failed = true
		case key == "" && p.KeyRef != "none" && p.KeyRef != "":
			fmt.Printf("key %-16s WARN  resolved empty (ref %s)\n", p.Name+":", secret.Redact(p.KeyRef))
		default:
			fmt.Printf("key %-16s OK    ref %s\n", p.Name+":", secret.Redact(p.KeyRef))
		}
	}

	addr := cfg.Settings.ServeListen
	if addr == "" {
		addr = "127.0.0.1:19777"
	}
	if err := validateServeListen(addr); err != nil {
		fmt.Printf("serve:     WARN  %s (%v)\n", addr, err)
	} else {
		fmt.Printf("serve:     OK    http://%s (/status.json, /usage.json)\n", addr)
	}

	var autoMeters int
	for _, p := range cfg.Providers {
		for _, m := range p.Meters {
			if m.Kind != "auto" || m.Auto == "" {
				continue
			}
			autoMeters++
			if m.Auto == "openrouter-credits" && (!strings.Contains(p.BaseURL, "openrouter.ai") || p.Kind != "openai-compatible") {
				fmt.Printf("meter %-14s WARN  %s is intended for OpenRouter\n", p.Name+"/"+m.Name+":", m.Auto)
			} else {
				fmt.Printf("meter %-14s OK    auto adapter %s\n", p.Name+"/"+m.Name+":", m.Auto)
			}
		}
	}
	fmt.Printf("moshi:     OK    %d auto meters; `sslug usage --format moshi`\n", autoMeters)

	if failed {
		os.Exit(1)
	}
}

func validateServeListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if port == "" {
		return fmt.Errorf("missing port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("must be loopback")
	}
	return nil
}

// --- sslug config path ---

func runConfig(args []string) {
	if len(args) > 0 && args[0] == "path" {
		fmt.Println(config.Path())
		return
	}
	fmt.Fprintln(os.Stderr, "usage: sslug config path")
	os.Exit(1)
}
