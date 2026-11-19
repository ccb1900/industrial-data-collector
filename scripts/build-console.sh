#!/usr/bin/env bash
# Build the shared console (go-cordis/web/console) and sync it into this
# application's embed directory. Applications never own frontend code; the
# console lives in the framework and is consumed as a build artifact.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
# Console 源码在相邻的框架仓库。默认 ../go-cordis，可用 GOCORDIS_CONSOLE 覆盖。
# 版本钉子是入库的 web/dist：它本身就是可复现的构建产物，重新构建前先看
# git diff web/dist——改了什么一目了然。脚本会把源码仓库的 HEAD 打进输出，
# 让每一次重建都可追溯到"哪个 commit 的 console"。
console="${GOCORDIS_CONSOLE:-$root/../go-cordis}/web/console"
if [ ! -d "$console" ]; then
  echo "error: shared console not found at $console (set GOCORDIS_CONSOLE to override)" >&2
  exit 1
fi
if head_sha="$(git -C "$(dirname "$console")" rev-parse --short HEAD 2>/dev/null)"; then
  echo "console source: $(dirname "$console") @ $head_sha"
else
  echo "console source: $(dirname "$console") @ (not a git repo)"
fi
(cd "$console" && npm install && npm run build)
rm -rf "$root/web/dist"
cp -R "$console/dist" "$root/web/dist"
echo "console synced into $root/web/dist"
echo "next: git diff --stat web/dist  # 审查本次 UI 产物变化，确认后入库"
