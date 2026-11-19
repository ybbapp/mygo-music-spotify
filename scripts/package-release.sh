#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
goos="${GOOS:-$(go env GOOS)}"
goarch="${GOARCH:-$(go env GOARCH)}"
ext=""
if [[ "$goos" == "windows" ]]; then
  ext=".exe"
fi

"$root/scripts/build-helper.sh"

dist="$root/dist"
mkdir -p "$dist"
name="mygo-music-spotify-${goos}-${goarch}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/bin"
cp "$root/plugin.json" "$root/index.js" "$root/README.md" "$root/README.zh-CN.md" "$root/THIRD_PARTY_NOTICES.md" "$stage/"
cp -R "$root/LICENSES" "$stage/"
cp "$root/bin/helper-${goos}-${goarch}${ext}" "$stage/bin/"
chmod 0755 "$stage/bin/helper-${goos}-${goarch}${ext}"
(
  cd "$stage"
  zip -qr "$dist/${name}.zip" .
)
printf '%s\n' "$dist/${name}.zip"

