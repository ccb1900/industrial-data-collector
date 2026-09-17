#!/bin/sh
# 升级 dynamic-runtime 到 gocordis 指定引用的最新提交（默认 main）。
#
# 模块名与仓库路径不一致（dynamic-runtime => github.com/ccb1900/gocordis），
# go get 动不了 replace，这里用 go list -m 解析引用对应的精确伪版本，
# 再改写 replace——手算时间戳这类事不再发生。
#
# 用法: scripts/bump-cordis.sh [main|v0.1.0|<commit>]
set -eu

ref=${1:-main}
repo=github.com/ccb1900/gocordis

v=$(go list -m -f '{{.Version}}' "$repo@$ref")
go mod edit -replace dynamic-runtime="$repo@$v"
go mod tidy
go build ./...
echo "dynamic-runtime => $repo $v"
