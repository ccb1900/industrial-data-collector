#!/usr/bin/env bash
# Build the shared console (go-cordis/web/console) and sync it into this
# application's embed directory. Applications never own frontend code; the
# console lives in the framework and is consumed as a build artifact.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
console="$root/../go-cordis/web/console"
if [ ! -d "$console" ]; then
  echo "error: shared console not found at $console" >&2
  exit 1
fi
(cd "$console" && npm install && npm run build)
rm -rf "$root/web/dist"
cp -R "$console/dist" "$root/web/dist"
echo "console synced into $root/web/dist"
