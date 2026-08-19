// sslug — btop-style LLM provider status TUI and CLI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chriisbusy/status-slug/internal/check"
	"github.com/chriisbusy/status-slug/internal/config"
	"github.com/chriisbusy/status-slug/internal/provider"
	"github.com/chriisbusy/status-slug/internal/secret"
	"github.com/chriisbusy/status-slug/internal/serve"
	"github.com/chriisbusy/status-slug/internal/state"
	"github.com/chriisbusy/status-slug/internal/tui/dashboard"
	"github.com/chriisbusy/status-slug/internal/tui/wizard"
)

var version = "dev"

func main() {
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
		fmt.Println("sslug", version)
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
	cfg, err := config.Load()
	if err != nil {
		fatal("load config", err)
	}
	// First run (or empty config): wizard before dashboard.
	if len(cfg.Providers) == 0 {
		cfg, err = wizard.Run(cfg, "")
		if err != nil {
			fatal("setup", err)
		}
		if len(cfg.Providers) == 0 {
			// User aborted wizard without adding anything.
			return
		}
	}
	if err := dashboard.Run(); err != nil {
		fatal("dashboard", err)
	}
}

func runSetup(args []string) {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	cfg := mustLoadConfig()
	if _, err := wizard.Run(cfg, name); err != nil {
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

	type target struct {
		prov    config.Provider
		model   string // empty = provider-level
		key     string
		adapter provider.Adapter
	}
	var targets []target
	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		if *providerFlag != "" && p.Name != *providerFlag {
			continue
		}
		key := resolveKey(p)
		adapter := provider.New(p.Kind)
		targets = append(targets, target{p, "", key, adapter})
		for _, m := range p.Models {
			if m.Favourite {
				targets = append(targets, target{p, m.ID, key, adapter})
			}
		}
	}

	if len(targets) == 0 {
		if *jsonFlag {
			fmt.Println(`{"schema":1,"results":[]}`)
		} else {
			fmt.Println("no providers configured — run: sslug setup")
		}
		return
	}

	ctx := context.Background()
	jobs := make([]check.Job, len(targets))
	for i, t := range targets {
		t := t
		jobs[i] = check.Job{
			Provider: t.prov.Name,
			ModelID:  t.model,
			Run: func(ctx context.Context) check.Result {
				doer := check.NewDoer(timeout, t.key)
				if t.model == "" {
					return t.adapter.Probe(ctx, doer, t.prov.BaseURL)
				}
				return t.adapter.ProbeModel(ctx, doer, t.prov.BaseURL, t.model)
			},
		}
	}

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

	// Fetch auto usage meters.
	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		if *providerFlag != "" && p.Name != *providerFlag {
			continue
		}
		for _, m := range p.Meters {
			if m.Kind != "auto" || m.Auto == "" {
				continue
			}
			key := resolveKey(p)
			doer := check.NewDoer(timeout, key)
			adapter := provider.New(p.Kind)
			ur, err := adapter.FetchUsage(ctx, doer, p.BaseURL, m.Auto)
			if err == nil && ur != nil {
				st.SetMeter(p.Name, m.Name, ur.Value)
			}
		}
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
	listen := fs.String("listen", "127.0.0.1:19777", "listen address (loopback only)")
	fs.Parse(args)

	mux := serve.NewMux()
	fmt.Printf("sslug serve listening on http://%s\n", *listen)
	if err := serve.ListenAndServe(*listen, mux); err != nil {
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

	if failed {
		os.Exit(1)
	}
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
