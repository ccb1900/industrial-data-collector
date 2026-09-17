#!/bin/sh
# 本地联调开关。gocordis 源码就在本机时，逐次 push + bump 太慢：
#
#   on  dynamic-runtime 直接指向 ../go-cordis 工作区——那边未提交的
#       改动立刻对本仓库的构建/测试生效，零 push。
#   off 恢复为远端版本（默认 main 最新，可传第二个参数指定引用）。
#
# 注意：on 状态下 go.mod 里是本机相对路径，不要把那个 go.mod 提交。
# 用法: scripts/cordis-dev.sh on | off [main|v0.1.0|<commit>]
set -eu

repo=github.com/ccb1900/gocordis

case ${1:-} in
  on)
    [ -d ../go-cordis ] || { echo "找不到 ../go-cordis 工作区" >&2; exit 1; }
    go mod edit -replace dynamic-runtime=../go-cordis
    go mod tidy
    echo "dynamic-runtime => ../go-cordis（本地联调模式，勿提交此时的 go.mod）"
    ;;
  off)
    v=${2:-$(go list -m -f '{{.Version}}' "$repo@main")}
    go mod edit -replace dynamic-runtime="$repo@$v"
    go mod tidy
    echo "dynamic-runtime => $repo $v"
    ;;
  *)
    echo "用法: $0 on | off [main|v0.1.0|<commit>]" >&2
    exit 2
    ;;
esac
