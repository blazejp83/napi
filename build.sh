#!/usr/bin/env bash
# Builds the Windows release (napi.exe + napiw.exe) into dist/ and zips it.
# Usage: ./build.sh [version]   e.g. ./build.sh 0.7.0
set -euo pipefail
cd "$(dirname "$0")"
version=${1:-$(git describe --tags --always 2>/dev/null || echo dev)}
ld="-s -w -X github.com/blaze/napi/internal/app.Version=$version"

# Windows resources: icon, version info and manifest, as .syso files that
# go build links in automatically. assets/napi.ico comes from tools/mkicon.
[[ -f assets/napi.ico ]] || go run ./tools/mkicon
numver=$version
[[ $numver =~ ^[0-9]+(\.[0-9]+){0,3}$ ]] || numver=0.0.0
winres() { # dir manifest description
  go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --out "$1/rsrc" --manifest "$2" \
    --icon assets/napi.ico --product-name Napi --file-description "$3" \
    --product-version "$numver" --file-version "$numver" --original-filename "$(basename "$1").exe"
}
winres cmd/napi cli "Napi – napisy do filmów (wiersz poleceń)"
# napiw: Wails loads the window icon from icon group ID 3, so it has its own
# resource file (cmd/napiw/winres.json) instead of the simple defaults.
go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64 --in cmd/napiw/winres.json --out cmd/napiw/rsrc \
  --product-version "$numver" --file-version "$numver"

mkdir -p dist
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$ld" -o dist/napi.exe ./cmd/napi
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags desktop,production -ldflags "$ld -H windowsgui" -o dist/napiw.exe ./cmd/napiw
(cd dist && rm -f "napi-$version-windows-amd64.zip" && zip -q "napi-$version-windows-amd64.zip" napi.exe napiw.exe)
ls -la dist
