# GOCORDIS — Configurable CSV Data Collection Application Specification v0.1

## 1. 目标

实现一个基于 GOCORDIS 的配置化 CSV 数据采集 Reference Application。

应用必须支持：

- CSV 文件采集
- 本地文件路径
- UNC 网络文件路径
- 配置化数据源
- 配置化 CSV 解析
- 配置化目标数据库
- MySQL
- PostgreSQL
- Oracle
- 每日定时执行
- 默认采集前一天数据
- 停机后自动发现遗漏日期
- 自动补采遗漏数据
- 幂等采集
- 配置动态变更
- Plugin 生命周期管理
- 采集任务失败恢复
- Runtime Close 后完整资源释放

本项目不是 CSV 库。

它是一个使用 GOCORDIS 构建完整 Application 的 Reference Implementation。

---

# 2. 非目标

本版本不实现：

- 实时文件监听
- Kafka
- MQTT
- Modbus
- OPC UA
- Excel
- JSON
- FTP
- SFTP
- 数据库 CDC
- 分布式任务调度
- 多 Agent
- Web UI
- LLM
- 数据清洗规则 DSL
- 数据质量平台
- 大数据计算

这些功能可以在后续 Application Plugin 中实现。

---

# 3. 总体架构

应用必须保持以下分层：

```text
                        CSV Collector Application
                                  │
                         GOCORDIS Runtime
                                  │
             ┌────────────────────┼────────────────────┐
             │                    │                    │
        Configuration          Scheduler            Storage
             │                    │                    │
             │                    │          ┌─────────┼─────────┐
             │                    │          │         │         │
             │                    │        MySQL   PostgreSQL   Oracle
             │                    │
             ▼                    ▼
        Collector ─────────── Collection Event
             │
             ▼
         FileSource
             │
       ┌─────┴─────┐
       │           │
     Local         UNC
       │           │
       └─────┬─────┘
             ▼
          CSV Parser
             │
             ▼
        Record / Batch
             │
             ▼
          Storage
```

---

# 4. Plugin 划分

至少实现以下 Plugin：

```text
Configuration Plugin
Scheduler Plugin
FileSource Plugin
CSV Parser Plugin
Collector Plugin
Storage Plugin
Recovery Plugin
```

其中：

```text
Configuration
Scheduler
FileSource
Parser
Collector
Storage
Recovery
```

都属于 Application Layer。

不得修改 GOCORDIS Kernel 以支持这些业务语义。

---

# 5. Capability 定义

Application 至少定义以下 Capability：

```text
FileSource
CSVParser
Storage
Scheduler
CollectionState
Collector
```

推荐接口：

```go
type FileSource interface {
    List(ctx context.Context, req ListRequest) ([]File, error)
    Read(ctx context.Context, file File) (io.ReadCloser, error)
}

type CSVParser interface {
    Parse(ctx context.Context, r io.Reader) (RecordStream, error)
}

type Storage interface {
    Write(ctx context.Context, batch Batch) error
}

type Scheduler interface {
    Schedule(ctx context.Context, spec ScheduleSpec, handler Handler) (Cleanup, error)
}

type CollectionState interface {
    IsCompleted(ctx context.Context, key CollectionKey) (bool, error)
    MarkCompleted(ctx context.Context, key CollectionKey) error
    ListIncomplete(ctx context.Context, scope CollectionScope) ([]CollectionKey, error)
}
```

实际 API 必须根据当前 Repository 的 Go API 风格确定。

上述接口用于定义契约，不得机械照抄。

---

# 6. Plugin 依赖关系

Collector 必须依赖：

```text
FileSource
CSVParser
Storage
CollectionState
```

关系：

```text
Collector
   │
   ├── FileSource
   ├── CSVParser
   ├── Storage
   └── CollectionState
```

Scheduler 不应该直接调用 Collector 的内部实现。

推荐：

```text
Scheduler
    │
    ▼
CollectionRequested Event
    │
    ▼
Collector
```

---

# 7. Storage Plugin

Storage 必须通过 Capability 提供。

实现：

```text
MySQLStorage
PostgreSQLStorage
OracleStorage
```

统一提供：

```text
Storage
```

Collector 不允许出现：

```go
switch databaseType {
case "mysql":
case "postgres":
case "oracle":
}
```

Collector 只能依赖：

```text
Storage
```

数据库差异必须封装在 Storage Plugin 内。

---

# 8. FileSource Plugin

必须实现：

```text
LocalFileSource
UNCFileSource
```

统一提供：

```text
FileSource
```

## 8.1 Local

支持：

```text
D:\data\2026-09-06\
```

## 8.2 UNC

支持：

```text
\\server\share\data\2026-09-06\
```

以及：

```text
\\192.168.1.100\share\data\2026-09-06\
```

UNC 路径必须作为普通配置值处理。

不得在 Collector 中编写 UNC 特殊逻辑。

---

# 9. 文件发现

FileSource 必须支持：

```text
根目录
+
日期路径
+
文件 Pattern
```

例如：

```text
\\server\share\production\2026-09-06\
```

Pattern：

```text
*.csv
```

最终得到：

```text
2026-09-06/
    a.csv
    b.csv
    c.csv
```

文件发现结果必须具有稳定标识。

推荐：

```text
FileIdentity
```

至少包含：

```text
source
path
size
modified time
```

如业务需要可增加：

```text
content hash
```

---

# 10. CSV Parser

CSV Parser 必须独立于 Collector。

至少支持：

- Header
- UTF-8
- CSV 标准字段解析
- 引号
- 字段中包含逗号
- 空字段
- CRLF
- LF

Parser 不负责：

- 数据库存储
- 日期判断
- 文件发现
- 调度
- 补采
- Runtime 生命周期

---

# 11. 数据模型

应用至少定义：

```text
CollectionKey
FileIdentity
Record
Batch
CollectionResult
```

推荐：

```go
type CollectionKey struct {
    SourceID string
    Date     time.Time
}
```

`CollectionKey` 表示：

> 某一个数据源在某一个业务日期的数据采集任务。

它不是 Runtime Fiber Identity。

必须严格区分：

```text
Runtime Identity
Application Collection Identity
File Identity
```

---

# 12. 配置模型

配置必须采用 TOML。

禁止 YAML。

示例：

```toml
[[components]]
id = "daily-orders"
type = "csv-collector"

[components.config]
source = "production"
pattern = "*.csv"
date_policy = "yesterday"
schedule = "daily"
storage = "mysql"
```

数据源：

```toml
[[components]]
id = "production-source"
type = "local-file-source"

[components.config]
root = "D:\\production\\data"
```

UNC：

```toml
[[components]]
id = "remote-production"
type = "unc-file-source"

[components.config]
root = "\\\\192.168.1.100\\production\\data"
```

数据库：

```toml
[[components]]
id = "mysql-storage"
type = "mysql-storage"

[components.config]
dsn = "..."
```

---

# 13. 配置与 Plugin Composition

配置不能直接实例化业务对象。

必须遵循：

```text
TOML
 ↓
Parse
 ↓
Validate
 ↓
Desired Components
 ↓
Factory
 ↓
GOCORDIS Component
 ↓
Runtime.Load / Reconciliation
 ↓
Fiber
```

配置错误必须在 Runtime 状态发生变化之前被拒绝。

---

# 14. 配置动态更新

配置发生变化：

```text
旧配置
   ↓
新配置
   ↓
Diff
   ↓
Reconcile
```

支持：

```text
Add
Remove
Replace
No-op
```

例如：

```toml
storage = "mysql"
```

修改为：

```toml
storage = "postgres"
```

必须产生：

```text
MySQL Storage
      ↓
withdraw
      ↓
PostgreSQL Storage
      ↓
activate
```

Collector 不修改。

---

# 15. Scheduler

Scheduler 是 Plugin。

默认支持：

```text
每天一次
```

例如：

```text
schedule = "daily"
time = "02:00"
```

Scheduler 触发：

```text
CollectionRequested
```

Collector 收到事件后执行采集。

Scheduler 不负责：

- CSV 解析
- 数据库写入
- 补采
- 文件扫描
- 业务日期计算

---

# 16. 默认采集策略

默认策略：

```text
当天运行
→ 采集昨天
```

例如：

```text
2026-09-07
      ↓
2026-09-06
```

必须定义：

```text
CollectionDatePolicy
```

至少：

```text
yesterday
specific
```

未来可扩展：

```text
range
latest-incomplete
```

---

# 17. 遗漏检测

应用必须能够识别遗漏日期。

例如：

```text
2026-09-01 ✓
2026-09-02 ✓
2026-09-03 ✓
2026-09-04 ✗
2026-09-05 ✗
2026-09-06 ✓
```

重新启动后：

```text
2026-09-07
    ↓
扫描 CollectionState
    ↓
发现：
09-04
09-05
```

然后：

```text
09-04 → collect
09-05 → collect
09-06 → collect / skip
```

---

# 18. Recovery

Recovery 必须是 Application 层能力。

Runtime 只保证：

```text
Plugin failure
Plugin unload
Plugin restart
Dependency recovery
Effect unwind
```

Collector 自己负责：

```text
业务日期
完成状态
遗漏检测
补采
```

不得向 Kernel 添加：

```text
RecoveryManager
CollectionManager
```

等领域抽象。

---

# 19. Collection State

必须持久化采集状态。

至少记录：

```text
source_id
collection_date
status
started_at
completed_at
error
```

状态：

```text
Pending
Running
Succeeded
Failed
```

推荐：

```text
CollectionKey
      ↓
Pending
      ↓
Running
      ↓
Succeeded
```

失败：

```text
Running
   ↓
Failed
```

下一次运行允许重新进入：

```text
Failed
  ↓
Running
  ↓
Succeeded
```

---

# 20. 幂等性

这是本应用的强制要求。

同一个：

```text
SourceID + Date
```

重复执行不能造成重复业务数据。

至少保证：

```text
同一 CollectionKey
重复采集
不会产生不可控重复写入
```

推荐数据库设计：

```text
collection_id
file_id
record_id
```

或业务唯一键。

Storage Plugin 必须提供幂等写入策略。

---

# 21. 文件级幂等

如果：

```text
09-06/a.csv
```

已经成功采集。

再次运行：

```text
09-06/a.csv
```

必须能够判断：

```text
Already Processed
```

避免重复写入。

推荐维护：

```text
FileIdentity
CollectionKey
```

之间的关系。

---

# 22. 采集执行模型

完整流程：

```text
Scheduler
    ↓
CollectionRequested
    ↓
Collector
    ↓
Resolve Collection Date
    ↓
Check CollectionState
    ↓
Discover Files
    ↓
Check File State
    ↓
Read CSV
    ↓
Parse
    ↓
Batch
    ↓
Storage.Write
    ↓
Mark File Completed
    ↓
Mark Collection Completed
```

---

# 23. 多文件处理

一个日期可以包含多个 CSV：

```text
2026-09-06/
    001.csv
    002.csv
    003.csv
```

必须定义：

```text
Collection
 ├── File 001
 ├── File 002
 └── File 003
```

单文件失败不能导致其他已经成功文件被重复处理。

例如：

```text
001 ✓
002 ✗
003 ✓
```

下一次：

```text
001 → Skip
002 → Retry
003 → Skip
```

---

# 24. 文件稳定性

采集程序不得读取仍在写入的 CSV。

必须支持：

```text
file_stable_window
```

例如：

```toml
file_stable_window_seconds = 30
```

文件至少在稳定窗口内：

```text
size unchanged
modified time unchanged
```

才允许采集。

---

# 25. 文件不存在

如果某个日期目录不存在：

必须区分：

```text
尚未产生
```

与：

```text
采集失败
```

不得简单记录为 Runtime Failure。

例如：

```text
2026-09-06 directory not found
```

Application 应根据配置决定：

```text
Pending
Skipped
Failed
```

默认建议：

> 不存在的数据日期保持可重新检查状态，而不是永久标记成功。

---

# 26. UNC 特殊要求

UNC Source 必须处理：

```text
网络暂时不可用
权限不足
路径不存在
连接超时
文件正在写入
```

错误必须归类。

至少：

```text
NotFound
PermissionDenied
Unavailable
Timeout
IOError
```

Collector 不应该依赖具体操作系统错误字符串。

---

# 27. 数据库错误

Storage 必须区分：

```text
Connection Error
Timeout
Authentication Error
Constraint Error
Transient Error
Permanent Error
```

Collector 不负责解析数据库厂商错误。

Storage Plugin 负责将错误映射到统一错误模型。

---

# 28. 批量写入

CSV 不允许默认：

```text
一行 → 一次数据库 INSERT
```

必须支持 Batch：

```text
CSV
 ↓
Record
 ↓
Batch
 ↓
Storage.Write
```

例如：

```text
batch_size = 1000
```

配置：

```toml
batch_size = 1000
```

---

# 29. 大文件处理

CSV Parser 必须采用 Streaming 思路。

禁止默认：

```text
Read entire file
→ []Record
```

导致整个 CSV 全部加载进内存。

推荐：

```text
Reader
 ↓
Parser
 ↓
Record Stream
 ↓
Batch
 ↓
Storage
```

---

# 30. Application Event

至少定义：

```text
CollectionRequested
CollectionStarted
FileDiscovered
FileStarted
FileCompleted
FileFailed
CollectionCompleted
CollectionFailed
```

Event payload 必须只携带 Application 数据。

例如：

```text
CollectionCompleted
{
    source_id
    date
    files
    records
    duration
}
```

不得让 Event Payload 直接暴露 Fiber / Activation / Runtime 内部对象。

---

# 31. Event 与生命周期

所有 Event Handler 必须通过 GOCORDIS Effect 注册。

Plugin 卸载后：

```text
Handler
 ↓
Unregistered
```

不得留下：

```text
goroutine
callback
channel
timer
```

继续引用已卸载 Plugin。

---

# 32. 并发模型

默认：

```text
一个 CollectionKey
→ 一个采集执行
```

不得同时执行：

```text
same source + same date
```

例如：

```text
Scheduler
    ↓
Recovery
    ↓
Manual Trigger
```

同时触发：

```text
source=A
date=2026-09-06
```

只能有一个有效执行。

---

# 33. Shutdown

Runtime Close：

```text
Runtime.Close()
```

必须最终导致：

```text
Scheduler stopped
Collector stopped
Event handlers removed
Storage closed
File handles closed
Worker goroutines stopped
Child components gone
Effects undone
```

最终：

```text
Quiescent
```

不得存在：

```text
采集 goroutine
Timer
DB connection
File handle
```

泄漏。

---

# 34. Failure Isolation

单个 Collector 失败：

```text
Collector A
    ↓
Failure
```

不能导致：

```text
Storage B
Scheduler C
Collector D
```

无条件失败。

Plugin failure 必须遵循 GOCORDIS Runtime 的既有生命周期语义。

---

# 35. Application Composition 示例

完整 Application：

```text
CSV Application
│
├── Config
│
├── Scheduler
│
├── FileSource
│
│   └── UNC
│
├── CSVParser
│
├── Collector
│
├── CollectionState
│
└── Storage
    ├── MySQL
    ├── PostgreSQL
    └── Oracle
```

---

# 36. 完整配置示例

必须提供一个完整可运行配置：

```toml
[[components]]
id = "production-source"
type = "unc-file-source"

[components.config]
root = "\\\\192.168.1.100\\production\\data"
pattern = "*.csv"


[[components]]
id = "csv-parser"
type = "csv-parser"

[components.config]
encoding = "utf-8"
header = true


[[components]]
id = "mysql-storage"
type = "mysql-storage"

[components.config]
dsn = "..." 
table = "production_data"
batch_size = 1000


[[components]]
id = "collection-state"
type = "collection-state"

[components.config]
database = "..."


[[components]]
id = "scheduler"
type = "scheduler"

[components.config]
schedule = "daily"
time = "02:00"


[[components]]
id = "production-collector"
type = "csv-collector"

[components.config]
source = "production-source"
parser = "csv-parser"
storage = "mysql-storage"
state = "collection-state"
date_policy = "yesterday"
file_stable_window_seconds = 30
batch_size = 1000
```

实际配置语法必须与当前 Config Extension 的真实实现保持一致。

上述内容是目标配置模型。

---

# 37. PostgreSQL 替换验证

将：

```toml
storage = "mysql-storage"
```

替换成：

```toml
storage = "postgres-storage"
```

Collector 源码不得修改。

必须验证：

```text
Collector
    │
    ▼
Storage Capability
    │
    ├── MySQL
    │
    └── PostgreSQL
```

---

# 38. UNC → Local 替换验证

将：

```text
UNCFileSource
```

替换为：

```text
LocalFileSource
```

Collector 源码不得修改。

必须验证：

```text
Collector
    ↓
FileSource
    ↓
Local
```

以及：

```text
Collector
    ↓
FileSource
    ↓
UNC
```

---

# 39. 配置动态替换

运行过程中：

```text
UNC
 ↓
Local
```

或者：

```text
MySQL
 ↓
PostgreSQL
```

必须通过 Config Reconciliation 完成。

不得：

```text
直接修改 Collector
直接修改 Fiber
直接修改 Provider Registry
```

---

# 40. 测试要求

必须至少包含以下测试层次。

## 40.1 Unit

测试：

```text
Date Policy
CollectionKey
FileIdentity
CSV Parser
State
Idempotency
Error Classification
```

## 40.2 Plugin

测试：

```text
FileSource
Storage
Scheduler
Collector
```

## 40.3 Integration

至少：

```text
CSV → Parser → Storage
```

## 40.4 Runtime Integration

至少：

```text
Config
→ Component
→ Dependency
→ Fiber
→ Active
→ Collection
→ Unload
→ Gone
```

---

# 41. 必须实现的 E2E 场景

### CSV-E2E-01

本地 CSV：

```text
Local
→ CSV
→ MySQL
```

成功。

### CSV-E2E-02

UNC：

```text
UNC
→ CSV
→ PostgreSQL
```

成功。

### CSV-E2E-03

昨日采集：

```text
Today
→ Yesterday
→ Collect
```

成功。

### CSV-E2E-04

停机恢复：

```text
D-3 ✓
D-2 ✗
D-1 ✗
Today
→ Recover
→ D-2 ✓
→ D-1 ✓
```

成功。

### CSV-E2E-05

重复运行：

```text
same date
→ execute twice
```

不得重复写入。

### CSV-E2E-06

多文件：

```text
A ✓
B ✓
C ✓
```

全部成功。

### CSV-E2E-07

单文件失败：

```text
A ✓
B ✗
C ✓
```

下一次：

```text
A skip
B retry
C skip
```

### CSV-E2E-08

Storage 替换：

```text
MySQL
→ PostgreSQL
```

Collector 无代码变化。

### CSV-E2E-09

Source 替换：

```text
UNC
→ Local
```

Collector 无代码变化。

### CSV-E2E-10

Config Reconciliation：

```text
old config
→ new config
→ Runtime convergence
```

成功。

### CSV-E2E-11

Scheduler：

```text
Schedule
→ Event
→ Collector
```

成功。

### CSV-E2E-12

Runtime Close：

```text
Close
→ all plugins Gone
→ resources released
```

成功。

---

# 42. GOCORDIS Conformance

该 Application 必须验证以下 Runtime 能力：

```text
Component
Fiber
Activation
Context
Capability
Dependency
Effect
Event
Realm
Ownership
Reconciliation
Cancellation
Failure
Quiescence
```

不得为了 Application 实现绕过这些机制。

---

# 43. Kernel Boundary Gate

实现完成后必须进行 Kernel Boundary Audit。

检查：

```text
runtime/
```

不得出现：

```text
CSV
Collector
Database
MySQL
PostgreSQL
Oracle
UNC
Scheduler
Collection
FileSource
```

任何新增 Kernel 代码都必须证明属于通用 Runtime 语义。

否则：

```text
FAIL
```

---

# 44. Application Boundary Gate

检查：

```text
examples/
application/
extensions/
```

中的业务代码是否错误承担 Runtime 生命周期。

禁止：

```go
fiber.Dispose()
```

作为 Plugin 内部生命周期控制手段。

禁止：

```go
runtime.orchestrator
```

禁止：

```go
runtime.providerRegistry
```

禁止直接修改其他 Plugin 生命周期。

必须通过 GOCORDIS Public API。

---

# 45. Resource Ownership Audit

必须检查：

```text
File
DB Connection
Timer
Ticker
Goroutine
Channel
Event Handler
Worker
```

每一个长期资源必须具有明确 Owner。

例如：

```text
Collector Activation
    │
    ├── Worker
    ├── File Handle
    └── Event Handler
```

卸载后全部释放。

---

# 46. Crash / Restart 语义

应用进程异常退出后重新启动：

```text
Persistent Collection State
        ↓
Recovery
        ↓
Detect incomplete
        ↓
Retry
```

不得依赖内存状态判断：

```text
哪些日期已经采集
哪些文件已经采集
```

这些状态必须持久化。

---

# 47. 数据一致性

至少保证：

```text
CollectionState
Storage
```

之间具有明确一致性语义。

禁止：

```text
先 MarkCompleted
再 Write Database
```

导致：

```text
State = Success
Data = Missing
```

推荐：

```text
Write Data
   ↓
Verify Success
   ↓
Mark Completed
```

对于跨数据库事务无法覆盖的场景，必须定义恢复策略。

---

# 48. 事务边界

必须明确：

```text
Batch
```

是最小 Storage 事务单元。

例如：

```text
Batch 1
→ transaction
→ commit

Batch 2
→ transaction
→ failure
```

则：

```text
Batch 1 = persisted
Batch 2 = retryable
```

不能假设整个 CSV 自动具有数据库事务。

---

# 49. 可观测性

本版本至少提供结构化日志：

```text
collection_id
source_id
date
file
records
duration
status
error
```

日志不得承担持久化状态职责。

即：

```text
Log != State
```

---

# 50. 配置错误处理

以下配置必须被拒绝：

```text
缺少 source
缺少 parser
缺少 storage
非法 schedule
非法 date_policy
不存在的 Component
不存在的 Capability
非法数据库配置
非法路径
非法 batch_size
```

错误必须在 Reconcile 修改 Runtime 前发现。

---

# 51. 失败恢复原则

所有失败必须明确属于：

```text
Configuration Failure
Dependency Failure
File Failure
Parser Failure
Storage Failure
Application Logic Failure
Runtime Lifecycle Failure
```

不得将所有错误统一为：

```text
collection failed
```

---

# 52. Plugin 生命周期原则

Collector Plugin：

```text
Load
 ↓
Dependency Waiting
 ↓
Active
 ↓
Collection
 ↓
Unload
 ↓
Effects Undo
 ↓
Gone
```

Scheduler：

```text
Load
 ↓
Active
 ↓
Schedule
 ↓
Unload
 ↓
Timer Stop
 ↓
Gone
```

Storage：

```text
Load
 ↓
Connect
 ↓
Active
 ↓
Unload
 ↓
Close Connection
 ↓
Gone
```

---

# 53. 不允许的实现方式

禁止：

```text
全局 Collector
全局 DB Connection
全局 Scheduler
全局 Event Bus
全局 File Registry
```

禁止：

```text
CollectorManager
StorageManager
SchedulerManager
RecoveryManager
PluginManager
```

除非这些对象明确属于 Application 内部普通服务，并且不承担第二套 Plugin Lifecycle。

---

# 54. 推荐代码结构

推荐：

```text
examples/csv-collector/
│
├── cmd/
│   └── csv-collector/
│       └── main.go
│
├── app/
│   ├── model/
│   ├── collector/
│   ├── source/
│   ├── parser/
│   ├── storage/
│   ├── scheduler/
│   ├── recovery/
│   └── config/
│
├── plugins/
│   ├── collector/
│   ├── source/
│   ├── parser/
│   ├── storage/
│   └── scheduler/
│
├── configs/
│   └── example.toml
│
└── README.md
```

实际目录可以根据 Repository 当前结构调整。

---

# 55. Application 启动流程

必须：

```text
main
 ↓
Create Runtime
 ↓
Register Application Component Factories
 ↓
Load Configuration
 ↓
Validate
 ↓
Create Desired Components
 ↓
Reconcile
 ↓
Wait Ready / Active
 ↓
Run
```

关闭：

```text
Signal
 ↓
Runtime.Close
 ↓
Wait Quiescence
 ↓
Exit
```

---

# 56. 完成标准

只有全部满足以下条件才允许标记：

```text
CSV Collector Reference Application — PASS
```

### Functional

```text
F-01 CSV
F-02 Local
F-03 UNC
F-04 Yesterday
F-05 Scheduling
F-06 Recovery
F-07 Idempotency
F-08 Multi-file
F-09 MySQL
F-10 PostgreSQL
F-11 Oracle
F-12 Dynamic Config
F-13 Replacement
F-14 Shutdown
```

### Runtime

```text
R-01 Component
R-02 Dependency
R-03 Capability
R-04 Effect
R-05 Event
R-06 Ownership
R-07 Scope
R-08 Reconciliation
R-09 Cancellation
R-10 Failure
R-11 Quiescence
```

### Boundary

```text
B-01 No domain concept in Kernel
B-02 No second lifecycle
B-03 No direct Fiber mutation
B-04 No direct Orchestrator access
B-05 No direct Provider Registry access
B-06 Public API only
```

### Reliability

```text
Q-01 Repeat execution
Q-02 Process restart
Q-03 Missing files
Q-04 UNC unavailable
Q-05 Database failure
Q-06 Partial file failure
Q-07 Shutdown
Q-08 Resource cleanup
```

---

# 57. 验证命令

必须执行：

```bash
go vet ./...
go test ./...
go test -race ./...
```

Application 自身必须提供：

```bash
go test ./examples/...
```

如果采用独立 module，则执行对应 module 的完整测试命令。

---

# 58. 最终交付物

实现完成后必须提交：

```text
1. Application 源码
2. Plugin 源码
3. TOML 示例
4. README
5. Architecture 文档
6. Configuration 文档
7. Recovery 文档
8. E2E Tests
9. Runtime Conformance Tests
10. Boundary Audit
11. Resource Ownership Audit
12. Test Results
13. Commit SHA
```

最终报告必须包含：

```text
Functional Gate
Runtime Gate
Boundary Gate
Reliability Gate
Race Gate
Resource Gate
```

每项：

```text
PASS / FAIL / CONDITIONAL PASS
```

并列出：

```text
测试文件
测试名称
验证内容
```

---

# 59. 最终架构判定标准

本项目最终不是为了证明：

> “我们可以写一个 CSV 采集器。”

而是为了证明：

```text
CSV Collector
      ↓
由 Plugin 组成
      ↓
Plugin 通过 Capability 协作
      ↓
Dependency 自动驱动生命周期
      ↓
Effect 管理所有副作用
      ↓
Event 解耦组件
      ↓
Configuration 描述 Desired Composition
      ↓
Reconciliation 驱动实际 Runtime
      ↓
Scheduler 触发业务行为
      ↓
Recovery 处理业务状态恢复
      ↓
Runtime 保证生命周期与资源收敛
```

因此，最终必须能够回答：

> **如果把 CSV Collector 换成 Web Application、AI Agent、ETL Pipeline 或其他复杂 Application，是否仍然可以使用完全相同的 GOCORDIS Runtime 模型？**

答案必须是：

```text
YES
```

如果为了实现 CSV Collector 而向 Kernel 添加 CSV、Scheduler、Database、Recovery 等领域概念，则本 Reference Application 判定为：

```text
FAIL
```

因为这意味着 GOCORDIS 的通用 Runtime 边界没有真正成立。

# 60. 实施优先级

严格按照：

```text
P0
├── Application Skeleton
├── Capability Contracts
├── Component / Plugin
└── Basic CSV Collection

P1
├── Configuration
├── Local / UNC Source
├── Storage
└── Scheduler

P2
├── Collection State
├── Idempotency
├── Yesterday Policy
└── Recovery

P3
├── Dynamic Reconciliation
├── Plugin Replacement
├── Failure Isolation
└── Runtime Shutdown

P4
├── Full E2E
├── Race
├── Resource Audit
└── Boundary Audit
```

不得在 P0/P1 阶段提前实现复杂的 Recovery、HMR 或数据库高级特性。

---

# 61. 最终用户体验

完成后，一个用户应该能够完成：

```text
1. 安装应用

2. 修改 config.toml

3. 指定：
   UNC 数据目录

4. 指定：
   CSV Pattern

5. 指定：
   数据库

6. 指定：
   每日执行时间

7. 启动应用

8. 应用每天采集昨天的数据

9. 应用停止若干天

10. 重新启动

11. 自动发现遗漏日期

12. 自动补采

13. 已采集数据不会重复写入

14. 修改配置即可更换数据库/数据源

15. 不修改 Collector 代码
```

这构成 GOCORDIS 第一个完整的、面向真实用户的 Reference Application。