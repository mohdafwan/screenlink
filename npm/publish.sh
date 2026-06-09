#!/usr/bin/env bash
# Publish the single screenlink package. Run ./npm/build.sh first, and
# `npm login` (or configure an automation token) beforehand.
#   ./npm/publish.sh            # real publish
#   ./npm/publish.sh --dry-run  # show what would be published, change nothing
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
DRY="${1:-}"
PKG="$HERE/screenlink"

if ! ls "$PKG"/vendor/*/screenlink* >/dev/null 2>&1; then
  echo "no bundled binaries — run ./npm/build.sh first" >&2
  exit 1
fi

echo "npm user: $(npm whoami 2>/dev/null || echo '(not logged in — run: npm login)')"
(cd "$PKG" && npm publish --access public $DRY)
echo "done."
