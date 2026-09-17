#!/bin/sh
# 升级 dynamic-runtime 到 gocordis 远端指定引用的最新提交（默认 main）。
#
# 模块名与仓库路径不一致（dynamic-runtime => github.com/ccb1900/gocordis），
# go get 动不了 replace，这里解析引用对应的精确伪版本再改写 replace。
#
# 解析用 GOPROXY=direct 直连 GitHub：模块代理对 @main 这类分支解析有
# 缓存，刚 push 完可能拿到旧提交；直连永远看到真实最新。下载仍走默认代理。
#
# 用法: scripts/bump-cordis.sh [main|v0.1.0|<commit>]
set -eu

ref=${1:-main}
repo=github.com/ccb1900/gocordis

v=$(GOPROXY=direct go list -m -f '{{.Version}}' "$repo@$ref")
go mod edit -replace dynamic-runtime="$repo@$v"
go mod tidy
go build ./...
echo "dynamic-runtime => $repo $v"
