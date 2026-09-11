#!/usr/bin/env bash
# Gera o binário de release para Linux x86_64 em dist/.
set -euo pipefail

version="${1:-$(grep -oP 'const version = "\K[^"]+' main.go)}"
mkdir -p dist

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o "dist/ff-linux-amd64" .

echo "dist/ff-linux-amd64  ($(du -h dist/ff-linux-amd64 | cut -f1))  versão $version"
