#!/usr/bin/env bash
# validate.sh — gofmt, vet, tests, secret scan. Exits non-zero on any failure.
set -euo pipefail
cd "$(dirname "$0")/../.."

echo "== gofmt =="
out=$(gofmt -l .)
if [ -n "$out" ]; then
    echo "gofmt needed on:"; echo "$out"; exit 1
fi
echo "  clean"

echo "== go vet =="
go vet ./...
echo "  clean"

echo "== go test =="
go test ./...
echo "  clean"

echo "== secret scan =="
if git rev-parse --git-dir >/dev/null 2>&1; then
    found=$(git grep -nE '(sk-[A-Za-z0-9]{20}|BEGIN (RSA |EC )?PRIVATE KEY)' -- . 2>/dev/null || true)
    if [ -n "$found" ]; then
        echo "SECRET LEAK:"; echo "$found"; exit 1
    fi
fi
echo "  clean"

echo "validate: all green"
