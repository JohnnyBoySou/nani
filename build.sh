#!/usr/bin/env bash
# Builds the release binary for Linux x86_64 into dist/.
set -euo pipefail

version="${1:-$(grep -oP 'const version = "\K[^"]+' main.go)}"
mkdir -p dist

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o "dist/nani-linux-amd64" .

echo "dist/nani-linux-amd64  ($(du -h dist/nani-linux-amd64 | cut -f1))  version $version"
