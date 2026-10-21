#!/usr/bin/env bash
# Builds the Windows release (napi.exe + napiw.exe) into dist/ and zips it.
set -euo pipefail
cd "$(dirname "$0")"
version=${1:-$(git describe --tags --always 2>/dev/null || echo dev)}
ld="-s -w -X github.com/blaze/napi/internal/app.Version=$version"
mkdir -p dist
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$ld" -o dist/napi.exe ./cmd/napi
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags desktop,production -ldflags "$ld -H windowsgui" -o dist/napiw.exe ./cmd/napiw
(cd dist && rm -f "napi-$version-windows-amd64.zip" && zip -q "napi-$version-windows-amd64.zip" napi.exe napiw.exe)
ls -la dist
