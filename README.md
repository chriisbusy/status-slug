<div align="center">

```
█▀▀ █▀▀ █   █ █ ▄▀▀
▄▄█ ▄▄█ █▄▄ █▄█ █▄█
```

# status-slug

**A btop-style status board for your AI providers — health, usage, and latency at a glance.**

[![ci](https://github.com/chriisbusy/status-slug/actions/workflows/ci.yml/badge.svg)](https://github.com/chriisbusy/status-slug/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/chriisbusy/status-slug.svg)](https://pkg.go.dev/github.com/chriisbusy/status-slug)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go 1.26+](https://img.shields.io/badge/go-1.26+-00ADD8.svg)](go.mod)

</div>

---

One command answers three questions you actually have about the providers you pay for
and depend on: **is it up**, **how much have I burned**, and **how fast is it right now**.

`sslug` watches any number of LLM providers — the mainstream clouds, the reseller
du jour, the box under your desk running Ollama, or a fully custom HTTP endpoint —
and presents their status, usage meters, and favourite-model latency in a single,
composable terminal dashboard. When you're not looking at the dashboard, the same
data feeds your tmux status line, your scripts, and your other tools over a
versioned JSON seam.

## Screenshot

![sslug dashboard — status, usage, favourites, and stats panes](docs/dashboard.png)

*Green/yellow/red health with reason text, per-provider usage meters with caps and
reset cycles, favourite-model latency sparklines, and a probe-statistics table.
Shown here against the built-in mock provider (`make mock`).*

## Install

**Go (recommended):**

```sh
go install github.com/chriisbusy/status-slug/cmd/sslug@latest
```

**From source:**

```sh
git clone https://github.com/chriisbusy/status-slug
cd status-slug
make build        # → ./sslug
```

Requirements: Go 1.26+ to build (see `go.mod`); any modern terminal to run. A
[Nerd Font](https://www.nerdfonts.com/) is optional — the default glyph set is
plain Unicode (`●◐○`, blocks, braille) and degrades gracefully (`NO_COLOR`
respected, non-truecolor terminals auto-degrade, narrow terminals reflow to a
stacked layout).

## Quick start

```sh
sslug            # first run: setup wizard (provider → key → models → meters)
sslug            # thereafter: the dashboard
sslug check      # probe everything now
sslug status     # last snapshot, no network
```

The wizard walks you through provider identity, API-key storage (OS keyring,
environment reference, or a plaintext file you explicitly opt into), model
discovery with favourite selection, and optional usage meters — then offers to
add another provider.

## The dashboard

Four panes, all configurable; presets cycle with `p`:

| Pane | What it shows |
|---|---|
| **status** | Provider health, p95 history, age, and row-level latency gauges |
| **usage** | Discrete square-cell meters with caps, reset cycles, and value age |
| **favourites** | Last latency, p95, and configurable TTY/block/braille history |
| **stats** | Dense provider/model process table with selection and viewport scrollbar |

Panes progressively admit as terminal space becomes available: one focused pane
at the minimum size, then status + stats, then usage, then all four. Drag pane
boundaries with the mouse to persist the top, left/right, and usage/favourites
split ratios per view.

Keys: `tab` cycle focus · `j/k` scroll · `c`/`⏎` check now · `i` inspect ·
`m` main menu · `s/u/f/t` pane menus · `p` view presets · `e` cycle themes
(live) · `g` integrations · `o` options · `a` add provider · `r` edit provider
(wizard popup) · `d` remove · `z` zoom pane · `?` help · `q` quit.

## Usage meters

Meters track anything with a number and a unit — spend, energy, requests,
credits — per provider:

```toml
[[providers.meters]]
name  = "Energy"
unit  = "kWh"          # any unit string
kind  = "manual"       # or "auto" where an adapter exists
cap   = 1000.0         # omit for uncapped
reset = "monthly:1"    # monthly:<day> | weekly:<mon..sun> | date:<YYYY-MM-DD> | never
```

Update manual meters from scripts or the TUI:

```sh
sslug usage set Neuralwatt Energy 231.5
```

Auto meters fetch themselves where a provider exposes an API — OpenRouter
credits ships in the box (`kind = "auto"`, `auto = "openrouter-credits"`).
Providers without a usage API get manual meters or none — sslug never
fabricates numbers.

## The JSON seam

Everything the dashboard knows is available to other tools, no network required
(reads the last persisted snapshot):

```sh
sslug status --format json     # full snapshot, "schema": 1
sslug status --format tmux     # ●3 ◐1 ○0  — drop into your status line
sslug usage  --format moshi    # moshi-hook-compatible usage snapshots
sslug serve                    # GET 127.0.0.1:19777/status.json + /usage.json
```

tmux status-right, for example:

```
set -g status-right '#(sslug status --format tmux) '
```

The `"schema": 1` field is a versioned contract: breaking shape changes bump it.

## Providers

Presets for OpenAI, OpenRouter, Anthropic, Google Gemini, Groq, DeepSeek,
Mistral, and Ollama (local, no key) — plus **custom**: any base URL with an
OpenAI-compatible `/models` endpoint works. Health classification is honest:
`401/403` and `402/insufficient_quota` are *account* problems (yellow), not
outages; `5xx`, timeouts, and DNS failures are *down* (red); `429` is a rate
limit, not a fire.

## Configuration & data

- Config: `~/.config/status-slug/config.toml` (`SSLUG_CONFIG_HOME` to override)
- State: `~/.local/state/status-slug/state.json` (`SSLUG_STATE_HOME`)
- Keys: OS keyring by default (service `sslug`); `env:VAR` references; or an
  opt-in 0600 file on headless systems. Keys are never printed, logged, or
  written outside the secret store.
- Themes: builtins `sstop` (default, btop-evoking), `mocha`, `nord`,
  `gruvbox`, `dracula`, `tokyonight`, `latte`, `mono`, plus your own
  `~/.config/status-slug/themes/<name>.theme` files — every colour role is
  configurable, invalid roles fall back with a warning, never a crash.
  `theme_background = true` paints the theme background (btop's
  `theme_background`); the default `false` lets your terminal's own
  background through. Light-background terminals automatically use `latte`.

## FAQ

**Do probes cost money?**
Provider health probes hit the free `GET /models` endpoint. Favourite-model
probes send a 1-token completion (`max_tokens: 1`, "ping") — about 1–2 tokens
per favourite per check. `check_on_launch` defaults to off.

**I'm headless / over SSH — no keyring?**
The wizard detects that and offers an explicit choice: a 0600-permission file
(with a plaintext-at-rest warning), an environment variable reference, or
abort. `sslug doctor` reports which store is live.

**Does it phone home?**
No telemetry, no analytics, no update checks. The only network calls are to
the provider endpoints you configured. This is a hard invariant, not a
preference.

**What does the traffic light actually mean?**
Green = a real probe succeeded. Yellow = your account needs attention
(auth, billing, or rate limit). Red = the service is unreachable or erroring.
Grey = never checked. Every state carries the reason and the time.

**Can it track my reseller / homelab / bespoke endpoint?**
That's the point of the `custom` kind: give it a base URL and, optionally,
a key. If it speaks `GET /models` and `POST /chat/completions`, it's a
first-class citizen.

**What's the Moshi integration status?**
`sslug usage --format moshi` emits moshi-hook-compatible usage snapshots
(`accountId`, `accountLabel`, `windows[]`, `cost{}`), and `sslug serve` exposes
the same snapshots over loopback at `/usage.json`. This producer side is
complete. Moshi does not currently provide a custom-source ingestion seam, so
automatic surfacing inside Moshi/iOS remains externally blocked as roadmap M9;
sslug does not imply that consumer-side support is already delivered.
The dashboard's `g` integrations view independently reads
`moshi-hook probe --json` and `moshi-hook status --json`, so daemon, gateway,
pairing, version, socket, and per-agent hook states are visible even though
custom usage ingestion remains blocked.

### Moshi daemon and hook setup

Prerequisite: `moshi-hook` must be installed and available on `PATH`.

1. In the Moshi iPhone app, open **Settings → Integrations** and create a
   pairing token.
2. Pair this host:

   ```sh
   moshi-hook pair --token '<pairing-token>' --name '<host-name>'
   ```

   On a headless Linux host, add `--store file` when no keychain is available.
3. Install and start the background daemon:

   ```sh
   moshi-hook service install
   ```

4. Install hooks for every detected supported agent, or choose explicit
   targets:

   ```sh
   moshi-hook install
   moshi-hook install --target claude,codex,omp
   ```

   Use `--local` to scope supported hook configurations to the current project.
5. Verify Moshi directly:

   ```sh
   moshi-hook probe
   moshi-hook status
   ```

6. Launch `sslug` and press `g`. The integrations view shows daemon, gateway,
   pairing, socket, version, and every hook state. Stale or missing hooks include
   the exact `moshi-hook install --target …` remediation command.

Status-slug only reads Moshi's JSON status interfaces. It never pairs hosts,
installs services, rewrites agent hooks, or syncs usage on the user's behalf.

## Acknowledgements

The design DNA — dense bordered panes, previous/current pair-cell graphs,
gradient chrome, and configurable TTY/block/braille rendering — is a loving tribute to
[**btop**](https://github.com/aristocratos/btop) by Aristocratos, which proved
system monitors could be beautiful. `sslug` applies the same craft to a
different machine room. Built on the [Charm](https://charm.land) stack
(Bubble Tea v2, Lip Gloss, Bubbles, Huh).

## License

MIT — see [LICENSE](LICENSE).
