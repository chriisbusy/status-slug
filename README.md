# status-slug

**sslug** — a btop-inspired terminal status board for LLM/AI providers and their models.

Not limited to mainstream providers: any OpenAI-compatible, Anthropic, Google, or fully custom HTTP endpoint works.

## Features

- **Dashboard TUI** — three-pane layout: provider status, usage meters, favourite-model latency cockpit with braille sparklines.
- **Setup wizard** — first launch guides you through provider, key, model discovery, and meter setup.
- **Usage meters** — manual (any unit: kWh, USD, requests, credits…) or auto-fetched (OpenRouter credits). Caps, reset schedules, age tracking.
- **Machine-readable CLI** — `status --json` / `status --format tmux` / `usage --format moshi` / `serve` loopback HTTP. Ready-made for tmux status lines, herdr plugins, moshi iOS sync.
- **Key storage** — OS keyring primary (D-Bus Secret Service), 0600 file fallback, env var references.
- **Themes** — builtin `sstop`, `nord`, `mono`; user `.theme` files; `NO_COLOR` respected.

## Install

```sh
go install github.com/chriisbusy/status-slug/cmd/sslug@latest
```

## Quick start

```sh
sslug           # first run → setup wizard
sslug setup     # add another provider
sslug check     # probe all providers now
sslug status    # last snapshot (no network)
```

## CLI reference

| Command | Description |
|---|---|
| `sslug` | Dashboard TUI |
| `sslug setup [name]` | Add or reconfigure a provider |
| `sslug check [--provider N] [--json] [--strict]` | Probe providers; `--strict` exits 3 on any non-green |
| `sslug status [--format plain\|tmux\|json]` | Last snapshot, no network |
| `sslug usage [--format plain\|json\|moshi]` | Meter snapshot, no network |
| `sslug usage set <provider> <meter> <value>` | Record a manual meter value |
| `sslug serve [--listen ADDR]` | Loopback HTTP (`127.0.0.1:19777`) — `GET /status.json`, `GET /usage.json` |
| `sslug providers` | List configured providers |
| `sslug remove <name>` | Delete provider + its secrets |
| `sslug doctor` | Diagnostics: config, keyring, key resolution |
| `sslug config path` | Print config file location |

## Configuration

Config: `~/.config/status-slug/config.toml` (override `SSLUG_CONFIG_HOME`).  
State: `~/.local/state/status-slug/state.json` (override `SSLUG_STATE_HOME`).

```toml
[settings]
theme                = "sstop"   # sstop | nord | mono | <file in themes/>
probe_timeout_seconds = 10
auto_refresh_seconds  = 60       # 0 = manual only
probe_mode           = "models"  # models | chat
history_length       = 60        # latency ring size per favourite (20–240)
keys_source          = "auto"    # auto | keyring | file | env
nerd_font            = false
border_style         = "rounded" # rounded | square | thick
graph_glyphs         = "braille" # braille | blocks | ascii
```

### Providers

```toml
[[providers]]
name     = "OpenAI"
kind     = "openai-compatible"   # openai-compatible | anthropic | google | custom
base_url = "https://api.openai.com/v1"
key_ref  = "keyring:openai"      # keyring:<id> | file:<id> | env:VAR | none
enabled  = true
note     = "60rpm plan"

[[providers.meters]]
name  = "Spend"
unit  = "USD"
kind  = "manual"        # manual | auto
used  = 12.50
cap   = 100.0
reset = "monthly:1"     # monthly:<day> | weekly:<mon..sun> | date:<YYYY-MM-DD> | never

[[providers.models]]
id        = "gpt-5-mini"
favourite = true
probe     = "chat"
```

### User themes

Drop a `<name>.theme` file in `~/.config/status-slug/themes/`, one `role = "#RRGGBB"` per line:

```
# my theme
ok     = "#00FF87"
warn   = "#FFD75F"
err    = "#FF5F5F"
accent = "#00C2FF"
```

Set `theme = "my"` in settings. Missing roles fall back to `sstop`.

### Keyring

Keys are stored in the OS keyring (service name `sslug`). On headless systems the wizard offers a 0600 file fallback or an env var reference. Keys are never printed, logged, or serialized outside the secret store.

### Probes cost tokens

Favourite-model probes use `max_tokens: 1` + `"ping"` — about 1–2 tokens per favourite per check. Provider-level probes use the free `GET /models` endpoint by default.

## Plugin integration

The `--json` surface carries `"schema": 1` and is the stable contract for external consumers (tmux status bars, herdr, moshi iOS, …). `sslug serve` exposes the same data over loopback HTTP for always-on consumers.

## License

MIT — see [LICENSE](LICENSE).
