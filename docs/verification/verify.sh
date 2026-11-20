#!/usr/bin/env bash
# 仓库自校验，两级：
#   第一级是硬门禁——go build / go vet / go test 全部必须绿，任何一红立即失败。
#   第二级是文档一致性——对《用户指南》《开发者指南》中每一条可机检的事实
#   （命令标志、hub 查询/命令、HTTP 端点、状态名、配置键、路径、脚本）逐条
#   grep 源码断言，任何一条 FAIL 都意味着文档与代码发生漂移。
#
# latest.txt 由本脚本生成，不许手写：手写的"PASS=141"在仓库演化两天后就是
# 负资产（2026-11-20 实测，旧 latest.txt 记的数字在当时的仓库上复现不出来）。
set -uo pipefail
cd "$(dirname "$0")/../.."
# latest.txt 随每次运行整体重写（表头承诺：不手写、不漂移）。
exec > >(tee docs/verification/latest.txt) 2>&1
pass=0; fail=0; skip=0

check() { # check <说明> <验证命令...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    pass=$((pass+1)); echo "PASS  $desc"
  else
    fail=$((fail+1)); echo "FAIL  $desc"
  fi
}
grep_q() { grep -q "$1" "$2"; }
skip_check() { # 前置物不存在时的诚实状态：跳过，而不是伪装成 FAIL
  skip=$((skip+1)); echo "SKIP  $1（$2）"
}

echo "== 硬门禁：go build =="
if go build ./... >/dev/null 2>&1; then pass=$((pass+1)); echo "PASS  go build ./..."; else fail=$((fail+1)); echo "FAIL  go build ./..."; fi
echo "== 硬门禁：go vet =="
if go vet ./... >/dev/null 2>&1; then pass=$((pass+1)); echo "PASS  go vet ./..."; else fail=$((fail+1)); echo "FAIL  go vet ./..."; fi
echo "== 硬门禁：go test =="
if go test ./... >/tmp/verify-go-test.log 2>&1; then pass=$((pass+1)); echo "PASS  go test ./..."; else fail=$((fail+1)); echo "FAIL  go test ./...（详见 /tmp/verify-go-test.log）"; fi

# 共享 Console 的源码在相邻的框架仓库（../go-cordis，与 scripts/build-console.sh
# 同一约定），可用 GOCORDIS_CONSOLE 覆盖。不存在时相关检查跳过——本仓库的
# UI 产物以 web/dist 为准（已入库，即版本钉子）。
CORDIS="${GOCORDIS_CONSOLE:-$(pwd)/../go-cordis}"

echo "== CLI 标志 =="
check "web-ui -config"      grep_q '"config", "configs/desktop.toml"' cmd/web-ui/main.go
check "web-ui -addr"        grep_q '"addr", ":8080"' cmd/web-ui/main.go
check "web-ui -dump-config" grep_q '"dump-config"' cmd/web-ui/main.go
check "web-ui -patch"       grep_q '"patch"' cmd/web-ui/main.go
check "csv-collector -once" grep_q '"once"' cmd/csv-collector/main.go
check "csv-collector -dump-config" grep_q '"dump-config"' cmd/csv-collector/main.go
check "csv-collector -patch" grep_q '"patch"' cmd/csv-collector/main.go
check "web-ui 启动补采"      grep_q 'app.Startup(ctx)' cmd/web-ui/main.go

echo "== hub 查询（consolebridge 注册）=="
for q in sources collections collection files failures observations schedule plan effective-config; do
  check "hub 查询 $q" grep_q "RegisterQuery(\"$q\"" components/consolebridge/component.go
done
check "hub 查询 logs（框架注册）" grep_q 'RegisterLogsQuery(hubRegistry' components/consolebridge/component.go
check "hub 命令 trigger" grep_q 'RegisterCommand("trigger"' components/consolebridge/component.go

echo "== 进程外插件 demo 契约 =="
for m in alarms alarm-stats ack-alarm; do
  check "proc 方法 $m" grep_q "\"$m\"" plugins/alarm-demo/main.go
done
check "manifest backend" grep_q 'backend = "./alarm-demo"' plugins/alarm-demo/manifest.toml
check "manifest client"  grep_q 'client = "ui.js"' plugins/alarm-demo/manifest.toml

echo "== webui 端点 =="
SV="$CORDIS/extensions/console/webui/server.go"
if [ ! -f "$SV" ]; then SV=/dev/null; UI_MISSING=1; else UI_MISSING=0; fi
for ep in '/api/ui/pages' '/api/ui/panels' '/api/ui/client-modules' '/api/plugins/control' '/api/plugins/install' '/api/plugins/removed' '/api/plugins/uninstall' '/api/meta' '/api/fleet' '/api/stream'; do
  if [ "$UI_MISSING" = 1 ]; then skip_check "端点 $ep" "找不到 $CORDIS"; else check "端点 $ep" grep_q "\"$ep\"" "$SV"; fi
done
if [ "$UI_MISSING" = 1 ]; then
  skip_check "webui 端点（9 条）" "找不到 $CORDIS"
else
check "GET /api/query/<名>"  grep_q '"/api/query/"' "$SV"
check "POST /api/command/<名>" grep_q '"/api/command/"' "$SV"
check "同源挂载 /client-modules/" grep_q '"/client-modules/"' "$SV"
fi

echo "== 状态集 =="
for st in Pending Running Succeeded Failed Skipped Expired; do
  check "状态 $st" grep_q "Status$st " app/model/model.go
done
BLOCKS="$CORDIS/web/console/src/views/blocks.tsx"
if [ -f "$BLOCKS" ]; then
check "UI 词：等待数据" grep_q '等待数据' "$BLOCKS"
check "UI 词：无数据"   grep_q '无数据' "$BLOCKS"
check "UI 词：成功"     grep_q '成功' "$BLOCKS"
else
skip_check "UI 词（3 条）" "找不到 $CORDIS"
fi

echo "== 日期策略与采集模式 =="
for v in yesterday today specific; do
  check "date_policy $v" grep_q "\"$v\"" app/date/policy.go
done
check "collection_mode batch"  grep_q 'collection_mode", "batch' components/sourceunit/component.go
check "collection_mode append" grep_q '"append"' components/sourceunit/component.go

echo "== source-unit 配置键 =="
# 键的读取位置随 Iteration F 分了家：parser 系（encoding/delimiter/skip_lines…）
# 在 app/parser/registry.go，存储系（driver/dsn/table/file_table/expose_console…）
# 在 components/storage/spec.go，其余仍在 sourceunit。文档一致性查的是
# "这个键真的有人读"，所以在全部装配面上 grep。
ASSEMBLY_FILES="components/sourceunit/component.go components/storage/spec.go app/parser/registry.go app/parser/text.go"
for k in source_id path pattern detect_content encoding header delimiter skip_lines date_policy specific_date catchup_days batch_size collection_mode file_stable_window_seconds dedupe_content_hash state_type state_dir storage sink driver dsn table file_table expose_console lazy_connect metadata_source layout machine format no_data_after_days; do
  # 不经 grep_q（它只收一个文件）：直接 grep -q 多文件，任一命中即 PASS。
  check "配置键 $k" grep -q "\"$k\"" $ASSEMBLY_FILES
done
  check "配置键 date_dir_layout" grep_q '"date_dir_layout"' components/sourceunit/component.go
  check "配置键 filename_date_layout" grep_q '"filename_date_layout"' components/sourceunit/component.go
check "四层展开函数" grep_q 'func expandFleet' app/sourcecomp/fleet.go
check "机台清单解析" grep_q 'func parseMachines' app/sourcecomp/fleet.go
check "sink 引用完整性" grep_q '引用了未知 sink' app/sourcecomp/fleet.go
check "脱敏哨兵" grep_q '__REDACTED__' app/host/redact.go
check "explorer 投影脱敏" grep_q 'SetDesired(redactConfigForDisplay(cfg))' app/host/host.go

echo "== 组件类型（manifest 自描述）=="
for pkg in watchtrigger storage scheduler metadata query ui-contrib consolebridge sourceunit config; do
  check "components/$pkg/manifest.toml" test -f "components/$pkg/manifest.toml"
done
total=$(python3 -c "import glob; print(sum(open(f).read().count('[[types]]') for f in glob.glob('components/*/manifest.toml')))")
check "manifest 类型总数 = 21（实际 ${total}）" test "$total" = "21"
check "pluginmeta 聚合存在" test -f internal/pluginmeta/pluginmeta.go
check "validate 聚合引用"   grep_q 'pluginmeta.Types()' app/config/validate.go

echo "== 页面与面板（collector-console 预设）=="
for pg in overview collect data; do
  check "页面 $pg" grep_q "\"page_id\":     \"$pg\"" app/bundles/collector.go
done
check "采集页为主从视图" grep_q '"kind": "master-detail"' app/bundles/collector.go
check "采集页含状态日历" grep_q '"kind": "calendar"' app/bundles/collector.go
for pn in logs event-feed; do
  check "面板 $pn" grep_q "\"panel_id\": \"$pn\"" app/bundles/collector.go
done
check "失败账本面板已并入采集页" test -z "$(grep -h 'ui-panel-failures' app/bundles/collector.go)"
check "详情右面板已并入采集页" test -z "$(grep -h 'ui-panel-collection-detail' app/bundles/collector.go)"
check "页面图标声明" grep_q '"icon":        "dashboard"' app/bundles/collector.go

echo "== 目录与脚本 =="
# 演示数据目录（data/…）是运行时产物、不入库——断言的是"配置引用它们"
# 这一文档事实，而不是"磁盘上恰好有演示文件"。
for p in configs/desktop.toml configs/laser.toml configs/unc-machines.toml plugins/alarm-demo/manifest.toml plugins/alarm-demo/main.go plugins/alarm-demo/src/ui.tsx scripts/build-console.sh scripts/install-plugin.sh docs/用户指南.md docs/开发者指南.md; do
  check "路径 $p" test -e "$p"
done
check "desktop 引用演示数据目录" grep_q 'data/production' configs/desktop.toml
check "desktop 引用质检数据目录" grep_q 'data/quality' configs/desktop.toml
check "补丁文件格式 version" grep_q '"version":1' docs/用户指南.md || true

echo "== go-cordis 框架扩展与设施 =="
GC="$CORDIS"
if [ ! -d "$GC" ]; then
  # 框架仓库不在相邻位置时诚实跳过：本仓库的 UI 产物以入库的 web/dist 为准
  #（它就是版本钉子），框架内部结构不因缺席而伪装成 FAIL。
  skip_check "go-cordis 框架（13 个 extensions 目录）" "找不到 $GC"
  skip_check "logstore/procplugin/plugin-kit" "找不到 $GC"
  skip_check "ComposeDocument / patch.ApplyPatches" "找不到 $GC"
  skip_check "Console 源码 seam（3 条）" "找不到 $GC"
else
for ext in broker bundle config configwatch console event hmr loader observe patch registry scheduler watch; do
  check "go-cordis extensions/$ext" test -d "$GC/extensions/$ext"
done
check "logstore 扩展"        test -d "$GC/extensions/console/logstore"
check "procplugin 扩展"      test -d "$GC/extensions/console/procplugin"
check "plugin-kit"           test -d "$GC/web/plugin-kit"
check "ComposeDocument"      grep_q 'func ComposeDocument' "$GC/extensions/configwatch/compose.go"
check "patch.ApplyPatches"   grep_q 'func ApplyPatches' "$GC/extensions/patch/patch.go"
check "渲染器注册表"          grep_q 'export function registerBlockRenderer' "$GC/web/console/src/views/registry.tsx"
check "投影 seam"            grep_q 'export function ingestObservation' "$GC/web/console/src/lib/projections.ts"
check "客户端模块加载"        grep_q 'export function reconcileClientModules' "$GC/web/console/src/lib/client-modules.ts"
fi

echo
echo "结果: PASS=$pass FAIL=$fail SKIP=$skip"
[ "$fail" -eq 0 ] && echo "文档与代码一致 ✔" || echo "存在漂移，需修复 ✘"
exit $((fail > 0))
