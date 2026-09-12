#!/usr/bin/env bash
# Install a console plugin (npm package) into plugins/.
#
# A plugin is an npm package whose package.json declares a client entry
# ("cordis"."client", conventionally ./ui.js). Installing = extracting the
# package into plugins/<name>/; if only sources ship, it builds in place
# (esbuild self-bundles the frontend and its dependencies).
#
# Usage:
#   scripts/install-plugin.sh @scope/my-plugin        # from the npm registry
#   scripts/install-plugin.sh ./my-plugin-0.1.0.tgz   # from a tarball (offline)
set -euo pipefail

spec="${1:?usage: install-plugin.sh <npm-spec|tarball>}"
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [[ -f "$spec" && "$spec" == *.tgz ]]; then
  cp "$spec" "$tmp/plugin.tgz"
else
  (cd "$tmp" && npm pack "$spec" >/dev/null)
fi
tar -xzf "$tmp/plugin.tgz" -C "$tmp"

name="$(node -p "require('$tmp/package/package.json').name")"
name="${name##*/}" # @scope/name installs as plugins/name

rm -rf "$root/plugins/$name"
mkdir -p "$root/plugins"
cp -R "$tmp/package" "$root/plugins/$name"

# The built artifact ships inside the package; a source-only package builds
# itself here (one npm install + its own build script).
if [[ ! -f "$root/plugins/$name/ui.js" ]] \
  && node -e "const p=require('$root/plugins/$name/package.json');process.exit(p.scripts&&p.scripts.build?0:1)"; then
  (cd "$root/plugins/$name" && npm install --silent && npm run build --silent)
fi

echo "installed: plugins/$name"
