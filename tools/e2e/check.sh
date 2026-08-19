#!/usr/bin/env bash
# e2e/check.sh — headless end-to-end test against mockprovider.
# Requires: ./sslug binary, mockprovider on :18821, jq.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SSLUG="$ROOT/sslug"
MOCK="$ROOT/mockprovider"
MOCK_PORT="${SSLUG_MOCK_PORT:-18821}"
SERVE_PORT=19778

# Temp config/state homes — no pollution.
export SSLUG_CONFIG_HOME="$(mktemp -d)"
export SSLUG_STATE_HOME="$(mktemp -d)"
trap 'rm -rf "$SSLUG_CONFIG_HOME" "$SSLUG_STATE_HOME"' EXIT

# Copy fixture config.
cp "$SCRIPT_DIR/fixture-config.toml" "$SSLUG_CONFIG_HOME/config.toml"

# Start mockprovider.
"$MOCK" &
MOCK_PID=$!
trap 'kill $MOCK_PID 2>/dev/null; rm -rf "$SSLUG_CONFIG_HOME" "$SSLUG_STATE_HOME"' EXIT

# Wait for mock.
for _ in $(seq 1 30); do
    if curl -sf "http://127.0.0.1:$MOCK_PORT/ok/v1/models" >/dev/null 2>&1; then break; fi
    sleep 0.2
done

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  PASS  $*"; }
fail() { FAIL=$((FAIL+1)); echo "  FAIL  $*"; }

echo "== sslug check --json =="
CHECK_OUT=$("$SSLUG" check --json 2>&1) || true
echo "$CHECK_OUT" | jq -e '.schema == 1' >/dev/null && ok "schema == 1" || fail "schema missing"
echo "$CHECK_OUT" | jq -e '.results[] | select(.provider=="MockOK" and .status=="ok")' >/dev/null && ok "MockOK ok" || fail "MockOK not ok"
echo "$CHECK_OUT" | jq -e '.results[] | select(.provider=="MockBilling" and .status=="account")' >/dev/null && ok "MockBilling account" || fail "MockBilling not account"
echo "$CHECK_OUT" | jq -e '.results[] | select(.provider=="MockDown" and .status=="down")' >/dev/null && ok "MockDown down" || fail "MockDown not down"

echo "== sslug usage set =="
"$SSLUG" usage set Neuralwatt Energy 231.5 >/dev/null && ok "usage set" || fail "usage set"

echo "== sslug status --json =="
STATUS_OUT=$("$SSLUG" status --format json)
echo "$STATUS_OUT" | jq -e '.schema == 1' >/dev/null && ok "status schema" || fail "status schema"
echo "$STATUS_OUT" | jq -e '.providers[] | select(.name=="Neuralwatt") | .meters[] | select(.name=="Energy") | .value == 231.5' >/dev/null && ok "meter value" || fail "meter value"
echo "$STATUS_OUT" | jq -e '.providers[] | select(.name=="Neuralwatt") | .meters[] | select(.name=="Energy") | .set_at' >/dev/null && ok "meter set_at" || fail "meter set_at"

echo "== sslug status --format tmux =="
TMUX_OUT=$("$SSLUG" status --format tmux)
echo "$TMUX_OUT" | grep -q '●1' && ok "tmux green count" || fail "tmux green count: $TMUX_OUT"
echo "$TMUX_OUT" | grep -q '◐1' && ok "tmux account count" || fail "tmux account count: $TMUX_OUT"
echo "$TMUX_OUT" | grep -q '○1' && ok "tmux down count" || fail "tmux down count: $TMUX_OUT"

echo "== sslug serve =="
"$SSLUG" serve --listen "127.0.0.1:$SERVE_PORT" &
SERVE_PID=$!
trap 'kill $MOCK_PID $SERVE_PID 2>/dev/null; rm -rf "$SSLUG_CONFIG_HOME" "$SSLUG_STATE_HOME"' EXIT
for _ in $(seq 1 20); do
    if curl -sf "http://127.0.0.1:$SERVE_PORT/status.json" >/dev/null 2>&1; then break; fi
    sleep 0.2
done
curl -sf "http://127.0.0.1:$SERVE_PORT/status.json" | jq -e '.schema == 1' >/dev/null && ok "serve /status.json schema" || fail "serve /status.json schema"
curl -sf "http://127.0.0.1:$SERVE_PORT/usage.json" | jq -e '.[0].windows[0].label' >/dev/null && ok "serve /usage.json" || fail "serve /usage.json"
kill $SERVE_PID 2>/dev/null || true

echo "== sslug doctor =="
"$SSLUG" doctor >/dev/null 2>&1 && ok "doctor exit 0" || fail "doctor exit non-zero"

echo ""
echo "e2e: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
