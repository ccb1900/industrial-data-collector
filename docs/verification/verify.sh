#!/usr/bin/env bash
# 文档代码级校验：对《用户指南》《开发者指南》中每一条可机检的事实
# （命令标志、hub 查询/命令、HTTP 端点、状态名、配置键、路径、脚本）
# 逐条 grep 源码断言。任何一条 FAIL 都意味着文档与代码发生漂移。
set -uo pipefail
cd "$(dirname "$0")/../.."
pass=0; fail=0

check() { # check <说明> <验证命令...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    pass=$((pass+1)); echo "PASS  $desc"
  else
    fail=$((fail+1)); echo "FAIL  $desc"
  fi
}
grep_q() { grep -q "$1" "$2"; }

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
SV=/Users/ccb1900/df/ui/opentusk/go-cordis/extensions/console/webui/server.go
for ep in '/api/ui/pages' '/api/ui/panels' '/api/ui/client-modules' '/api/plugins/control' '/api/plugins/install' '/api/plugins/removed' '/api/plugins/uninstall' '/api/meta' '/api/fleet' '/api/stream'; do
  check "端点 $ep" grep_q "\"$ep\"" "$SV"
done
check "GET /api/query/<名>"  grep_q '"/api/query/"' "$SV"
check "POST /api/command/<名>" grep_q '"/api/command/"' "$SV"
check "同源挂载 /client-modules/" grep_q '"/client-modules/"' "$SV"

echo "== 状态集 =="
for st in Pending Running Succeeded Failed Skipped; do
  check "状态 $st" grep_q "Status$st " app/model/model.go
done
check "UI 词：等待数据" grep_q '等待数据' /Users/ccb1900/df/ui/opentusk/go-cordis/web/console/src/views/blocks.tsx
check "UI 词：无数据"   grep_q '无数据' /Users/ccb1900/df/ui/opentusk/go-cordis/web/console/src/views/blocks.tsx
check "UI 词：成功"     grep_q '成功' /Users/ccb1900/df/ui/opentusk/go-cordis/web/console/src/views/blocks.tsx

echo "== 日期策略与采集模式 =="
for v in yesterday today specific; do
  check "date_policy $v" grep_q "\"$v\"" app/date/policy.go
done
check "collection_mode batch"  grep_q 'collection_mode", "batch' components/sourceunit/component.go
check "collection_mode append" grep_q '"append"' components/sourceunit/component.go

echo "== source-unit 配置键 =="
for k in source_id path pattern detect_content encoding header delimiter skip_lines date_policy specific_date catchup_days batch_size collection_mode file_stable_window_seconds dedupe_content_hash state_type state_dir storage sink driver dsn table file_table expose_console lazy_connect metadata_source layout; do
  check "配置键 $k" grep_q "\"$k\"" components/sourceunit/component.go
done
check "配置键 no_data_grace_hours" grep_q '"no_data_grace_hours"' components/sourceunit/component.go
check "脱敏哨兵" grep_q '__REDACTED__' app/host/redact.go
check "explorer 投影脱敏" grep_q 'SetDesired(redactConfigForDisplay(cfg))' app/host/host.go

echo "== 组件类型（manifest 自描述）=="
for pkg in source parser watchtrigger storage state scheduler collector metadata query ui-contrib consolebridge sourceunit config; do
  check "components/$pkg/manifest.toml" test -f "components/$pkg/manifest.toml"
done
total=$(python3 -c "import glob; print(sum(open(f).read().count('[[types]]') for f in glob.glob('components/*/manifest.toml')))")
check "manifest 类型总数 = 27（实际 ${total}）" test "$total" = "27"
check "pluginmeta 聚合存在" test -f internal/pluginmeta/pluginmeta.go
check "validate 聚合引用"   grep_q 'pluginmeta.Types()' app/config/validate.go

echo "== 页面与面板（collector-console 预设）=="
for pg in overview collections sources data; do
  check "页面 $pg" grep_q "\"page_id\":     \"$pg\"" app/bundles/collector.go
done
check "无独立文件页（主从合并）" test -z "$(grep -h 'ui-page-files' app/bundles/collector.go)"
for pn in logs event-feed failures collection-detail; do
  check "面板 $pn" grep_q "\"panel_id\": \"$pn\"\|\"panel_id\": \"$pn\"" app/bundles/collector.go
done
check "详情面板绑定 collections" grep_q '"pages":    \[\]any{"collections"}' app/bundles/collector.go
check "页面图标声明" grep_q '"icon":        "dashboard"' app/bundles/collector.go

echo "== 目录与脚本 =="
for p in configs/desktop.toml configs/example.toml configs/windows-task.toml data/production data/quality plugins/alarm-demo/manifest.toml plugins/alarm-demo/main.go plugins/alarm-demo/src/ui.tsx scripts/build-console.sh scripts/install-plugin.sh docs/用户指南.md docs/开发者指南.md; do
  check "路径 $p" test -e "$p"
done
check "补丁文件格式 version" grep_q '"version":1' docs/用户指南.md || true

echo "== go-cordis 框架扩展与设施 =="
GC=/Users/ccb1900/df/ui/opentusk/go-cordis
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
check "客户端模块加载"        grep_q 'export async function loadClientModules' "$GC/web/console/src/lib/client-modules.ts"

echo
echo "结果: PASS=$pass FAIL=$fail"
[ "$fail" -eq 0 ] && echo "文档与代码一致 ✔" || echo "存在漂移，需修复 ✘"
exit $((fail > 0))
