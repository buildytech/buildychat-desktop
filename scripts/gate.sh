#!/usr/bin/env bash
# Local gate before a push to main (no --force, no --no-verify): formatting, vet, the unit and contract tests, the
# submodule pin, and the headless (server) build. The GUI build runs in CI on all three systems.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
[ "$(git branch --show-current)" = "main" ] || { echo "gate: branch must be main" >&2; exit 1; }
git submodule update --init third_party/chat
[ -z "$(git -C third_party/chat status --porcelain --untracked-files=no)" ] || { echo "gate: third_party/chat has local changes" >&2; exit 1; }
unformatted="$(gofmt -l assets.go cmd internal)"
[ -z "$unformatted" ] || { echo "gate: gofmt needed: $unformatted" >&2; exit 1; }
PKGS="./ ./internal/... ./cmd/..."
# nodynamic: the WebP encoder is gen2brain/webp's embedded libwebp (WASM), never a system libwebp, so every build encodes alike.
TAGS="nodynamic"
go vet -tags "$TAGS" $PKGS
go test -tags "$TAGS" -count=1 ./ ./internal/... ./cmd/...
passed="$(go test -tags "$TAGS" -count=1 -run TestRuntimeHTTPContract -v ./internal/runtime | grep -c -- '--- PASS: TestRuntimeHTTPContract/')"
echo "gate: desktop contract cases passed: $passed"
go build -tags "server $TAGS" -o bin/buildychat-server ./cmd/buildychat
echo "gate: ok (chat $(git -C third_party/chat rev-parse --short HEAD))"
