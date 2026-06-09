#!/usr/bin/env bash
# Cross-compile the Go binary for every supported platform and bundle them all
# into the single screenlink npm package under npm/screenlink/vendor/. Linux
# targets also get capture.py next to the binary (the host looks for it there).
# Run this before ./npm/publish.sh.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
GO="${GO:-$HOME/.go-sdk/go/bin/go}"
VENDOR="$HERE/screenlink/vendor"
cd "$ROOT"

targets="linux-x64 linux-arm64 darwin-x64 darwin-arm64 win32-x64"

rm -rf "$VENDOR"
for t in $targets; do
  plat="${t%-*}"; arch="${t#*-}"
  case "$plat" in
    linux)  goos=linux ;;
    darwin) goos=darwin ;;
    win32)  goos=windows ;;
    *) echo "unknown platform: $plat" >&2; exit 1 ;;
  esac
  case "$arch" in
    x64)   goarch=amd64 ;;
    arm64) goarch=arm64 ;;
    *) echo "unknown arch: $arch" >&2; exit 1 ;;
  esac

  dir="$VENDOR/$t"
  mkdir -p "$dir"
  out="$dir/screenlink"
  [ "$plat" = "win32" ] && out="$out.exe"

  echo "==> $t  ($goos/$goarch)"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    "$GO" build -trimpath -ldflags "-s -w" -o "$out" .
  [ "$plat" != "win32" ] && chmod 0755 "$out"

  # capture.py must sit next to the binary for `screenlink host` (Linux only).
  if [ "$plat" = "linux" ]; then
    cp "$ROOT/capture.py" "$dir/capture.py"
  fi

  du -h "$out" | awk '{print "    "$1"\t"$2}'
done

echo "done — binaries bundled in npm/screenlink/vendor/"
