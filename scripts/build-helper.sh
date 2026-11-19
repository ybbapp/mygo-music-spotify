#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
export GOTOOLCHAIN=auto
goos="${GOOS:-$(go env GOOS)}"
goarch="${GOARCH:-$(go env GOARCH)}"
ext=""
if [[ "$goos" == "windows" ]]; then
  ext=".exe"
fi

out="$root/bin/helper-${goos}-${goarch}${ext}"
mkdir -p "$root/bin"
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
  -trimpath \
  -ldflags="-s -w" \
  -o "$out" \
  ./helper
chmod 0755 "$out"
printf '%s\n' "$out"

