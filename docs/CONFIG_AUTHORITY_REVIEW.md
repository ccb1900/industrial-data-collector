# 配置权威核查报告（Iteration 配置-1 前置，2026-11-20）

只读核查，五点全部有代码证据。这份报告定义了权威收敛（G0）的语义边界。

## 1. 文件编辑 vs fleetstore：文件的四层表是"死信"，且无人知晓

`app/host/watch.go:25-40`（validatingParser.Parse）：每次文件重载都会
`rememberSeed(data)` → `fleetBase()`——**fleet_doc 非空时，文件的四层表
（machines/formats/format_groups/sinks/schedules/defaults）被账本文档整体
覆盖**（`doc.AsBase()` 作为展开基准，注释原文"控制台编辑的真相"）。

后果：操作员手改 TOML 的 `[[machines]]` → 文件热重载成功 → 运行时重建 →
**但机台层用的是陈旧的账本文档，改动静默无效**。没有任何日志、任何事件指出
这一点；唯一的出口是控制台的"恢复 TOML"（fleet.reset）。非 fleet 部分
（组件、补丁）仍走文件——同一份文件一半生效一半死信。

**G0 的最小正确语义**：保持"账本覆盖文件"的权威（这是既有决定、现场已在
用），但把死信变响——文件重载时对比 seed 与账本诞生时的文件指纹，不一致即
告警（指明哪些层被遮蔽、给出去处）。不改变权威，只消灭无知。

## 2. reconcile 失败：没有回滚，失败组件停在 Failed

- `FleetMutate`（host.go:148-206）：干跑校验（组合展开 → 补丁叠加 → 应用层
  校验）失败即拒绝、账本保持原样——**配置级错误永远不会到达运行时**。
- 运行时级失败（如 DSN 能连上配置但连不上库）发生在组件 Apply → Fiber
  Failed → `PostReconcile` 的 30s 就绪检查（hReadyBound）→ notify → 返回
  err。**没有"回退到上一个可用组合"**：失败的组件停在 Failed，该源停采，
  恢复 = 修配置 + 再调和。逐组件编辑路径有补丁回滚（host.go:554-560），fleet
  级文档没有。
- 失败通知链存在：`SetReconcileFailureSink` → cmd/web-ui 挂了观察流。

**结论**："失败时保留旧的可用运行状态"是**新能力**，不是现状描述。G 的
`last_apply` 先如实记录（哪一版、成功/失败、错误），回滚能力列为后续评估——
它需要框架层面的组合快照与恢复，不是配置层能独立完成的。

## 3. fleet.set 与表单命令同源：确认

`components/consolebridge/fleet.go`：`fleet.set`（整文档）、`fleet.reset`、
`fleet.machine.add`、`fleet.schedule.add`、格式/组/sink 的删除命令——全部
经同一组处理器（`mutateFleetRows` 包装行编辑，最终走 `FleetMutate`）。
校验链只有一份（干跑三段）。CAS 与审计在 FleetMutate 一处加盖即可覆盖全部
入口。

## 4. 凭证以明文 JSON 落 config.db

`FleetSnapshot` 展示时脱敏（`redactFleet`，哨兵 `__REDACTED__`）；
`FleetMutate` 提交时从 `h.unredactedFleet()` 还原真实值再校验、**再落库**
（`store.Save(doc)` 存的是还原后的文档）。即 config.db 的 JSON 里是明文
DSN/口令。脱敏 machinery 已存在 → 导出快照可以安全脱敏；凭证出库
（Credential Manager）是它真正的修复，列 P2。

## 5. 渲染协议：动态表单需要框架侧配合（边界如实）

本仓库的声明式页面块类型：form/table/stats/kv/json/trend/master-detail/
query-table/calendar/event-feed（`app/bundles/collector.go`）。"Schema 驱动
的动态表单"需要块渲染器支持由查询提供字段定义——blocks.tsx 在框架仓库
（`gocordis/web/console`，本环境不存在），属于 GoCordis 侧改动。本仓库能做
的是把 Schema 以后端契约供出（与工厂同注册）；页面接线留在 UI 阶段。

## 6. 附带发现：第三条配置写入面

`applyOverlay`（host.go:363+）：生效配置 = seed 文件 + 控制台 overlay 层 +
**操作员 `--patch` 层**（注释原文"操作员层有最终发言权"）。权威收敛的设计
必须把这一层算进去——它优先级最高且合理（现场应急），保留，但要写进文档，
否则"配置到底有几个人能改"的答案永远数不清。
