#!/usr/bin/env bash
# Build the LLM Provider Checker Windows GUI executable.
# Produces provider-checker.exe in the project root.
set -euo pipefail

cd "$(dirname "$0")"

echo "==> go test ./..."
go test ./...

echo "==> Building for windows/amd64 (pure Go, no CGO needed)..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w -H windowsgui" \
  -o provider-checker.exe \
  .

echo "==> Done."
ls -la provider-checker.exe
