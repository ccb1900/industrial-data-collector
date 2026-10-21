// Typed relational sink: maps CSV data columns and metadata keys onto real
// database columns. No JSON blobs — every column is typed and queryable.
// Two idempotency backbones are available:
//
//   - legacy (default): (source_id, collection_date, file_id, row_number).
//     file_id is the full identity string, so it repeats ~77 bytes on every
//     row — and repeats again inside the composite-PK autoindex.
//   - file_key mode (`file_key = true`): the data table stores
//     (file_key, row_number) where file_key is an integer generated in-process
//     (Snowflake layout, see snowflake.go) and registered once per file in the
//     file registry table, which becomes load-bearing. The
//     per-row duplication of path/source/date disappears; measured on the
//     production shape this cuts the database by roughly half.
//
// The CSV header of each collected file is stored once in the file registry
// table as plain TEXT.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// ColumnSource selects where a mapped column takes its value from.
type ColumnSource string

const (
	SourceCSV      ColumnSource = "csv"
	SourceMetadata ColumnSource = "metadata"
)

// ColumnMapping declares one typed database column fed from the CSV data
// header or from the merged business metadata of the file.
type ColumnMapping struct {
	From       ColumnSource `toml:"from"`
	Name       string       `toml:"name"`       // source key: CSV header field or metadata key
	Column     string       `toml:"column"`     // database column name
	Type       string       `toml:"type"`       // text|int|bigint|float|bool|timestamp|date
	Required   bool         `toml:"required"`   // missing value fails the file
	Occurrence int          `toml:"occurrence"` // csv 源：取表头名的第 N 次出现（0 起）；重名表头用它区分
	// Optional 豁免表头校验：声明了本列但文件表头里没有它时不再告警。
	// 用于同一格式存在代际差异（如三代 49 列、六代 50 列的并集声明）。
	// 缺列只是取空值，本身不阻断；与 Required 互斥（必填列不许豁免）。
	Optional bool `toml:"optional"`
}

// TableConfig configures the typed relational sink.
type TableConfig struct {
	Driver    string
	DSN       string
	Dialect   string
	Table     string
	FileTable string          // per-file registry (header as TEXT); required by FileKey
	Columns   []ColumnMapping // typed columns
	ExtraRows bool            // store unmapped CSV fields into row_values TEXT
	Exposer   bool            // expose the RowsQuery capability for consoles
	Lazy      bool            // defer connection to first use
	// AutoColumns 自动字段映射：Columns 未声明时，按解码表头建列（全部
	// TEXT，列名即表头文本；重名表头第 k(k>=2) 次出现加后缀 __k），后续
	// 文件出现新表头字段时增量 ALTER 补列。声明了 Columns 时本开关无效。
	AutoColumns bool
	// FileKey 用整型外键替代逐行的 file_id 文本：数据表主键变成
	// (file_key, row_number)，file_key 由进程内雪花发号器生成（见
	// snowflake.go），注册表按键回查复用。需要 FileTable（注册表从可选
	// 变为必需）。
	FileKey bool
	// FileKeyNode 固定雪花键的 16 位节点号（0–65535）。发号器按注册表在
	// 进程内单例：同一 (driver, DSN, file_table) 的所有 source unit 共用一个，
	// 所以本字段是**注册表级**配置——共享同一注册表的源必须全部省略它，或
	// 全部钉同一个值；出现冲突值 OpenTable 返回 ErrInvalidConfig。缺省时
	// 每个进程对每个注册表随机取一个节点号。多进程部署时各进程仍各自随机，
	// 跨进程同节点同毫秒同序号由 (file_key) 唯一索引兜底。只在"要让键可
	// 复现"时才填。
	FileKeyNode *int
	// HeaderCheck 表头与声明列的失配处置："strict"（默认，Validate 补齐）
	// = 表头有、声明没有的字段判整个文件失败；"warn" = 只告警不阻断。
	// 方向只有这一个：声明有、表头没有的列取空值，恒为告警（列级 optional
	// 可豁免），不参与本开关。见 headerCheck。
	HeaderCheck string
}

func (c *TableConfig) Validate() error {
	if c.Driver == "" || c.DSN == "" {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage driver/dsn required")
	}
	c.Dialect = strings.ToLower(c.Dialect)
	switch c.Dialect {
	case "postgres", "mysql", "sqlite", "oracle", "sqlserver":
	default:
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage dialect %q not supported (postgres/mysql/sqlite/oracle/sqlserver)", c.Dialect)
	}
	if c.Table == "" {
		c.Table = "records"
	}
	c.HeaderCheck = strings.ToLower(strings.TrimSpace(c.HeaderCheck))
	switch c.HeaderCheck {
	case "":
		c.HeaderCheck = "strict"
	case "strict", "warn":
	default:
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage %q: header_check must be strict or warn, got %q", c.Table, c.HeaderCheck)
	}
	if !tableNamePattern.MatchString(c.Table) {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage table %q is not a simple identifier", c.Table)
	}
	if c.FileTable != "" && !tableNamePattern.MatchString(c.FileTable) {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage file_table %q is not a simple identifier", c.FileTable)
	}
	if c.FileKey {
		// 注册表是键的归属地（按键回查、唯一索引兜底），从可选变为必需。
		if c.FileTable == "" {
			return errs.Sourcef(errs.ErrInvalidConfig, "table storage %q: file_key mode requires file_table (the registry allocates the keys)", c.Table)
		}
		if c.FileKeyNode != nil && (*c.FileKeyNode < 0 || *c.FileKeyNode > flakeNodeMax) {
			return errs.Sourcef(errs.ErrInvalidConfig, "table storage %q: file_key_node must be within 0..%d, got %d", c.Table, flakeNodeMax, *c.FileKeyNode)
		}
	}
	if len(c.Columns) == 0 && !c.AutoColumns {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage %q: no columns declared (set columns or auto_columns)", c.Table)
	}
	seen := map[string]bool{}
	seenSrc := map[string]bool{}
	for i := range c.Columns {
		col := &c.Columns[i]
		switch col.From {
		case SourceCSV, SourceMetadata:
		case "":
			col.From = SourceCSV
		default:
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q: from must be csv or metadata", col.Column)
		}
		if col.Name == "" {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q: name required", col.Column)
		}
		if col.Occurrence < 0 {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q: occurrence must be >= 0", col.Column)
		}
		if col.Optional && col.Required {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q: optional and required contradict (required already fails the file when the header lacks it)", col.Column)
		}
		if col.From == SourceMetadata && col.Occurrence != 0 {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q: occurrence applies to csv sources only", col.Column)
		}
		if !tableNamePattern.MatchString(col.Column) {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q is not a simple identifier", col.Column)
		}
		if seen[col.Column] {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q declared twice", col.Column)
		}
		seen[col.Column] = true
		if col.From == SourceCSV {
			src := col.Name + "\x00" + strconv.Itoa(col.Occurrence)
			if seenSrc[src] {
				return errs.Sourcef(errs.ErrInvalidConfig, "csv source %q occurrence %d declared twice", col.Name, col.Occurrence)
			}
			seenSrc[src] = true
		}
		switch col.Type {
		case "text":
			col.Type = "text"
		case "int", "integer":
			col.Type = "int"
		case "bigint":
			col.Type = "bigint"
		case "float", "double", "real":
			col.Type = "float"
		case "bool", "boolean":
			col.Type = "bool"
		case "timestamp", "timestamptz":
			col.Type = "timestamp"
		case "date":
			col.Type = "date"
		default:
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q: unsupported type %q (text|int|bigint|float|bool|timestamp|date)", col.Column, col.Type)
		}
	}
	return nil
}

// RowsPage is one page of queried table rows for the console explorer.
type RowsPage struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	Total   int64           `json:"total"`
}

// TableStat is one table's row count in the typed sink.
type TableStat struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// StorageStats is the storage insight view: connectivity plus per-table row
// counts of the typed sink.
type StorageStats struct {
	Connected bool        `json:"connected"`
	Tables    []TableStat `json:"tables"`
}

// RowsQuery is the console-facing read side of the typed sink.
type RowsQuery interface {
	// QueryRows pages collected rows; an empty sourceID or date means that
	// dimension is unfiltered (cross-batch queries). filters are
	// exact-match column filters (declared columns only).
	QueryRows(ctx context.Context, sourceID, date string, limit, offset int, filters map[string]string) (RowsPage, error)
	// Stats reports connectivity and per-table row counts for the storage
	// insight cards.
	Stats(ctx context.Context) (StorageStats, error)
}

// TableStorage is the typed relational sink.
type TableStorage struct {
	db      *sql.DB
	lazy    bool
	cfg     TableConfig
	closeMe func() error

	// 自动字段映射：columns 未声明时，以解码表头建列并可随后续文件增量
	// 加列（同名文件同表、同名字段同列、新字段 ALTER 补列）。
	// effectiveColumns 是全部读路径的列来源；原子指针保证控制台查询
	// 与采集写入并发时的可见性与无竞争。evoMu 串行化演进（合并列集 +
	// ensureSchema 补建），写路径本身按源串行。
	autoCols atomic.Pointer[[]ColumnMapping]
	evoMu    sync.Mutex
	// 声明式 columns + auto_columns 并存时声明优先，多余表头字段告警。
	driftWarned map[string]bool // 写路径串行访问
	// fileKeyGen 是雪花发号器，schema 阶段立好（initFileKeyGen）。
	// 原子指针：多个源并发首写时先到先得，后到者复用同一实例。
	fileKeyGen atomic.Pointer[fileKeyGen]
}

// effectiveColumns returns the column set in force: declared columns win;
// with AutoColumns the first batch's decoded header derives them.
func (t *TableStorage) effectiveColumns() []ColumnMapping {
	if cs := t.autoCols.Load(); cs != nil {
		return *cs
	}
	return t.cfg.Columns
}

// deriveColumns 从解码表头生成列声明：Name 保留表头原文（mapRow 按表头
// 名取值），Column 即表头文本（quoteIdent 负责安全引用）。空白列跳过。
// 重名表头全部物化：首次出现用裸名，第 k (k>=2) 次出现加后缀 __k；
// 若候选名与既有表头/既有生成名冲突则继续递增后缀，保证全表唯一。
func deriveColumns(header []string) []ColumnMapping {
	cols := make([]ColumnMapping, 0, len(header))
	used := map[string]bool{}
	count := map[string]int{}
	for _, raw := range header {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		name = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1 // 控制字符不进标识符
			}
			return r
		}, name)
		name = strings.TrimSpace(name)
		if name == "" {
			continue // 剥离控制字符后为空（如纯 \x01 的表头）——跳过
		}
		occ := count[name]
		count[name]++
		colName := name
		if occ > 0 {
			for n := occ + 1; ; n++ {
				cand := name + "__" + strconv.Itoa(n)
				if !used[cand] {
					colName = cand
					break
				}
			}
		}
		used[colName] = true
		cols = append(cols, ColumnMapping{From: SourceCSV, Name: raw, Column: colName, Type: "text", Occurrence: occ})
	}
	return cols
}

// evolveAutoColumns 把 header 派生出的新列（按 表头名+出现次数 认定）并入
// 现有自动列集，并借 ensureSchema 的增量 ALTER 补建到库。补列失败即回退
// 列集——写路径绝不引用库里不存在的列，下一批次自然重试。
func (t *TableStorage) evolveAutoColumns(ctx context.Context, header []string) error {
	derived := deriveColumns(header)
	t.evoMu.Lock()
	defer t.evoMu.Unlock()
	prev := t.autoCols.Load()
	merged, grew := mergeDerivedCols(prev, derived)
	if !grew {
		return nil
	}
	t.autoCols.Store(&merged)
	if err := t.ensureSchema(ctx); err != nil {
		t.autoCols.Store(prev)
		return err
	}
	if prev != nil {
		slog.Info("auto-mapped table grew columns",
			"table", t.cfg.Table, "added", len(merged)-len(*prev), "total", len(merged))
	}
	return nil
}

// mergeDerivedCols 在 existing 基础上并入 derived 中尚未物化的 (Name,
// Occurrence) 对；列名沿用派生名，与既有列冲突则按 __k 顺延（大小写不
// 敏感——多数目录按小写比对列名）。
func mergeDerivedCols(existing *[]ColumnMapping, derived []ColumnMapping) ([]ColumnMapping, bool) {
	out := []ColumnMapping{}
	if existing != nil {
		out = append(out, *existing...)
	}
	have := make(map[string]bool, len(out)*2)
	used := make(map[string]bool, len(out)*2)
	for _, c := range out {
		have[c.Name+"\x00"+strconv.Itoa(c.Occurrence)] = true
		used[strings.ToLower(c.Column)] = true
	}
	grew := false
	for _, d := range derived {
		key := d.Name + "\x00" + strconv.Itoa(d.Occurrence)
		if have[key] {
			continue
		}
		have[key] = true
		col := d.Column
		for n := d.Occurrence + 1; used[strings.ToLower(col)]; n++ {
			col = d.Name + "__" + strconv.Itoa(n+1)
		}
		used[strings.ToLower(col)] = true
		nc := d
		nc.Column = col
		out = append(out, nc)
		grew = true
	}
	return out, grew
}

// OpenTable opens the typed sink eagerly.
func OpenTable(ctx context.Context, cfg TableConfig) (*TableStorage, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ensureSQLiteDir(cfg)
	// 连接池按 driver+dsn 进程级共享（引用计数）：机台群展开后源单元
	// 数量远大于物理库数量，每源独享池会把空闲会话堆到数据库上限。
	db, release, err := acquireDB(cfg.Dialect, cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		release()
		return nil, errs.ClassifyStorageError("ping", err)
	}
	t := &TableStorage{db: db, cfg: cfg, closeMe: releaseClose(release)}
	if err := t.ensureSchema(ctx); err != nil {
		_ = t.Close()
		return nil, err
	}
	return t, nil
}

// EnsureConnected opens the connection for a lazy store (open + ping + schema).
func (t *TableStorage) EnsureConnected(ctx context.Context) error {
	if t.db != nil {
		return nil
	}
	ensureSQLiteDir(t.cfg)
	db, release, err := acquireDB(t.cfg.Dialect, t.cfg.Driver, t.cfg.DSN)
	if err != nil {
		return err
	}
	if err := db.PingContext(ctx); err != nil {
		release()
		return errs.ClassifyStorageError("ping", err)
	}
	t.db = db
	t.closeMe = releaseClose(release)
	if err := t.ensureSchema(ctx); err != nil {
		// schema 失败（含 file_key_node 冲突）必须回到未连接态：否则下一次
		// EnsureConnected 看到 db != nil 会误判初始化已完成，带着半套表结构
		// 进入写路径。
		_ = t.Close()
		return err
	}
	return nil
}

// OpenTableLazy defers connection to the first write (same semantics as the
// generic SQL sink's lazy mode).
func OpenTableLazy(cfg TableConfig) (*TableStorage, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &TableStorage{lazy: true, cfg: cfg}, nil
}

// dialectTypes maps a logical type to the dialect's DDL type.
func dialectTypes(dialect string) map[string]string {
	switch dialect {
	case "postgres":
		return map[string]string{"text": "TEXT", "int": "INTEGER", "bigint": "BIGINT", "float": "DOUBLE PRECISION", "bool": "BOOLEAN", "timestamp": "TIMESTAMPTZ", "date": "DATE"}
	case "mysql":
		return map[string]string{"text": "TEXT", "int": "INT", "bigint": "BIGINT", "float": "DOUBLE", "bool": "TINYINT(1)", "timestamp": "DATETIME", "date": "DATE"}
	case "oracle":
		return map[string]string{"text": "VARCHAR2(4000)", "int": "NUMBER(10)", "bigint": "NUMBER(19)", "float": "BINARY_DOUBLE", "bool": "NUMBER(1)", "timestamp": "TIMESTAMP", "date": "DATE"}
	case "sqlserver":
		// NVARCHAR(4000) 承载中文表头列名对应的数据；IDENTITY 不用，
		// 主键是业务四元组。
		return map[string]string{"text": "NVARCHAR(4000)", "int": "INT", "bigint": "BIGINT", "float": "FLOAT", "bool": "BIT", "timestamp": "DATETIME2", "date": "DATE"}
	default: // sqlite
		return map[string]string{"text": "TEXT", "int": "INTEGER", "bigint": "INTEGER", "float": "REAL", "bool": "INTEGER", "timestamp": "TEXT", "date": "TEXT"}
	}
}

func (t *TableStorage) ensureSchema(ctx context.Context) error {
	dt := dialectTypes(t.cfg.Dialect)
	oracle := t.cfg.Dialect == "oracle"
	mssql := t.cfg.Dialect == "sqlserver"
	// Data table: idempotency backbone + declared columns. Oracle 的键列
	// 用定长类型：PK 索引键超长会 ORA-01450，不能用 VARCHAR2(4000)。
	var cols []string
	pk := "PRIMARY KEY (source_id, collection_date, file_id, row_number)"
	switch {
	case t.cfg.FileKey:
		cols = []string{
			"file_key " + dt["bigint"] + " NOT NULL",
			"row_number " + dt["bigint"] + " NOT NULL",
		}
		pk = "PRIMARY KEY (file_key, row_number)"
	case oracle:
		cols = []string{
			"source_id VARCHAR2(255) NOT NULL",
			"collection_date DATE NOT NULL",
			"file_id VARCHAR2(255) NOT NULL",
			"row_number NUMBER(19) NOT NULL",
		}
	case mssql:
		// SQL Server 的键列必须定宽有界：聚集索引键上限实测 900 字节，超了
		// 到 INSERT 才报 1946，而 dt["text"] 的 NVARCHAR(4000) 直接进主键
		// 必然踩线。file_id 要装得下完整文件身份（含长 UNC 路径），取 512，
		// 相应把主键声明为非聚集（实测最坏 1291 字节可过）。
		cols = []string{
			"source_id NVARCHAR(128) NOT NULL",
			"collection_date DATE NOT NULL",
			"file_id NVARCHAR(512) NOT NULL",
			"row_number BIGINT NOT NULL",
		}
		pk = "PRIMARY KEY NONCLUSTERED (source_id, collection_date, file_id, row_number)"
	default:
		cols = []string{
			"source_id " + dt["text"] + " NOT NULL",
			"collection_date " + dt["date"] + " NOT NULL",
			"file_id " + dt["text"] + " NOT NULL",
			"row_number " + dt["bigint"] + " NOT NULL",
		}
	}
	for _, c := range t.effectiveColumns() {
		cols = append(cols, quoteIdent(t.cfg.Dialect, c.Column)+" "+dt[c.Type])
	}
	if t.cfg.ExtraRows {
		cols = append(cols, "row_values "+dt["text"])
	}
	if oracle || mssql {
		// Oracle / SQL Server 无 IF NOT EXISTS：查系统目录后按需建表。
		if !t.tableExists(ctx, t.cfg.Table) {
			if err := t.execDDL(ctx, fmt.Sprintf("CREATE TABLE %s (%s, %s)", quoteIdent(t.cfg.Dialect, t.cfg.Table), strings.Join(cols, ", "), pk)); err != nil {
				return err
			}
		}
	} else if err := t.execDDL(ctx, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s, %s)", quoteIdent(t.cfg.Dialect, t.cfg.Table), strings.Join(cols, ", "), pk)); err != nil {
		return err
	}
	// Additive evolution: add declared columns that exist neither in the
	// CREATE (new config) nor in the table (old config).
	existing, err := t.existingColumns(ctx, t.cfg.Table)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, c := range existing {
		have[strings.ToLower(c)] = true
	}
	// 存量表不能悄悄换骨架：旧表带 file_id 却无 file_key，按新骨架写入会
	// 在第一批就撞"无此列"。这里在增量 ALTER 之前判定并给出可执行的错误，
	// 迁移（回填 file_key + 重建数据表）是一次离线动作，不该由采集进程顺手做。
	if t.cfg.FileKey && have["file_id"] {
		return errs.Sourcef(errs.ErrInvalidConfig,
			"table %q already exists with the legacy (source_id, collection_date, file_id, row_number) backbone; file_key mode needs a table created with (file_key, row_number) — migrate it (see docs/OPERATIONS.md) or point this sink at a new table",
			t.cfg.Table)
	}
	for _, c := range t.effectiveColumns() {
		if have[strings.ToLower(c.Column)] {
			continue
		}
		ddl := fmt.Sprintf("ALTER TABLE %s ADD %s %s", quoteIdent(t.cfg.Dialect, t.cfg.Table), quoteIdent(t.cfg.Dialect, c.Column), dt[c.Type])
		if oracle {
			ddl = fmt.Sprintf("ALTER TABLE %s ADD (%s %s)", quoteIdent(t.cfg.Dialect, t.cfg.Table), quoteIdent(t.cfg.Dialect, c.Column), dt[c.Type])
		}
		if err := t.execDDL(ctx, ddl); err != nil {
			return err
		}
	}
	// File registry (optional): one row per collected file, header as TEXT.
	if t.cfg.FileTable != "" {
		ft := quoteIdent(t.cfg.Dialect, t.cfg.FileTable)
		fileDDL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (source_id %s NOT NULL, collection_date %s NOT NULL, file_id %s NOT NULL, path %s, name %s, records %s, header %s, collected_at %s, PRIMARY KEY (source_id, collection_date, file_id))",
			ft, dt["text"], dt["date"], dt["text"], dt["text"], dt["text"], dt["bigint"], dt["text"], dt["timestamp"])
		if oracle {
			// 键列换 Oracle 安全类型；无 IF NOT EXISTS，查 user_tables。
			if !t.tableExists(ctx, t.cfg.FileTable) {
				fileDDL = fmt.Sprintf("CREATE TABLE %s (source_id VARCHAR2(255) NOT NULL, collection_date DATE NOT NULL, file_id VARCHAR2(255) NOT NULL, path VARCHAR2(1000), name VARCHAR2(255), records NUMBER(19), header VARCHAR2(4000), collected_at TIMESTAMP, PRIMARY KEY (source_id, collection_date, file_id))", ft)
			} else {
				fileDDL = ""
			}
		} else if mssql {
			// 同 oracle：无 IF NOT EXISTS，查 sys.objects。键列宽度与非聚集
			// 主键的取舍同数据表（见上方 ensureSchema 的 mssql 分支）。
			if !t.tableExists(ctx, t.cfg.FileTable) {
				fileDDL = fmt.Sprintf("CREATE TABLE %s (source_id NVARCHAR(128) NOT NULL, collection_date DATE NOT NULL, file_id NVARCHAR(512) NOT NULL, path NVARCHAR(1000), name NVARCHAR(255), records BIGINT, header NVARCHAR(4000), collected_at DATETIME2, PRIMARY KEY NONCLUSTERED (source_id, collection_date, file_id))", ft)
			} else {
				fileDDL = ""
			}
		}
		if fileDDL != "" {
			if err := t.execDDL(ctx, fileDDL); err != nil {
				return err
			}
		}
		// file_key 模式的键位：注册表加一列整型键 + 唯一索引。
		// 存量注册表走 ADD COLUMN（旧行键为 NULL，被引用时由 resolveFileKey
		// 就地补一个雪花键）。旧的小键与新的大键共存：键只要求唯一，
		// 不需要连续，所以换发号方案不是一次迁移。
		if t.cfg.FileKey {
			rcols, err := t.existingColumns(ctx, t.cfg.FileTable)
			if err != nil {
				return err
			}
			hasKey := false
			for _, c := range rcols {
				if strings.EqualFold(c, "file_key") {
					hasKey = true
				}
			}
			if !hasKey {
				if err := t.execDDL(ctx, fmt.Sprintf("ALTER TABLE %s ADD file_key %s", ft, dt["bigint"])); err != nil {
					return err
				}
			}
			// 唯一约束是键分配的最后防线：分配若被并发击穿，这里报错而不是
			// 让两个文件共用一个键（那会让数据行静默挂到别的文件上）。
			// IF NOT EXISTS 只有 sqlite/postgres（含 MariaDB）支持——
			// oracle/sqlserver 查系统目录按需建。
			uq := quoteIdent(t.cfg.Dialect, t.cfg.FileTable+"_file_key_uq")
			uqDDL := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (file_key)", uq, ft)
			switch t.cfg.Dialect {
			case "oracle":
				// PL/SQL 匿名块必须以 END; 收尾，缺分号会在解析期报 PLS-00103。
				uqDDL = fmt.Sprintf("BEGIN EXECUTE IMMEDIATE 'CREATE UNIQUE INDEX %s ON %s (file_key)'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -955 THEN RAISE; END IF; END;", uq, ft)
			case "sqlserver":
				uqDDL = fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.indexes WHERE name = '%s' AND object_id = OBJECT_ID('%s')) CREATE UNIQUE INDEX %s ON %s (file_key)",
					t.cfg.FileTable+"_file_key_uq", t.cfg.FileTable, uq, ft)
			case "mysql":
				uqDDL = fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (file_key)", uq, ft)
			}
			if err := t.execDDL(ctx, uqDDL); err != nil {
				return err
			}
			// 发号器在 schema 阶段立好：读一次 MAX(file_key) 作基线，
			// 之后每批发号零往返。计数器表（<file_table>_file_key_seq）
			// 已退役——旧方案在写事务里 UPDATE 同一行、X 锁持到整批改写
			// 提交，并发全部排在热点行上（现场实测 15 会话 LCK_M_X 等
			// 8–13 秒），见 snowflake 小节的注释与 docs/OPERATIONS.md。
			// 存量库里这张表还在，只是不再被读写，可按需 DROP。
			if err := t.initFileKeyGen(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// tableExists reports whether the table already exists (Oracle dialect).
func (t *TableStorage) tableExists(ctx context.Context, table string) bool {
	var cnt int
	var q string
	switch t.cfg.Dialect {
	case "oracle":
		q = "SELECT COUNT(*) FROM user_tables WHERE table_name = UPPER(:1)"
	case "sqlserver":
		q = "SELECT COUNT(*) FROM sys.objects WHERE object_id = OBJECT_ID(@p1)"
	default:
		// sqlite/mysql/postgres 走 CREATE IF NOT EXISTS 路径，不查目录。
		return false
	}
	if err := t.db.QueryRowContext(ctx, q, table).Scan(&cnt); err != nil {
		return false
	}
	return cnt > 0
}

func (t *TableStorage) existingColumns(ctx context.Context, table string) ([]string, error) {
	var out []string
	var err error
	switch t.cfg.Dialect {
	case "sqlite":
		rows, e := t.db.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(t.cfg.Dialect, table)+")")
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, ctype string
			var notNull, pk int
			var dflt any
			if e := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); e != nil {
				return nil, e
			}
			out = append(out, name)
		}
		err = rows.Err()
	case "sqlserver":
		rows, e := t.db.QueryContext(ctx,
			"SELECT name FROM sys.columns WHERE object_id = OBJECT_ID(@p1) ORDER BY column_id", table)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if e := rows.Scan(&name); e != nil {
				return nil, e
			}
			out = append(out, name)
		}
		err = rows.Err()
	case "oracle": // user_tab_columns；未加引号建成的表名/列名均为大写
		rows, e := t.db.QueryContext(ctx,
			"SELECT column_name FROM user_tab_columns WHERE table_name = UPPER(:1)", table)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if e := rows.Scan(&name); e != nil {
				return nil, e
			}
			out = append(out, name)
		}
		err = rows.Err()
	default: // mysql: information_schema（占位符 ?）
		rows, e := t.db.QueryContext(ctx,
			"SELECT column_name FROM information_schema.columns WHERE table_name = ?", table)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if e := rows.Scan(&name); e != nil {
				return nil, e
			}
			out = append(out, name)
		}
		err = rows.Err()
	case "postgres": // information_schema（占位符 $1）
		rows, e := t.db.QueryContext(ctx,
			"SELECT column_name FROM information_schema.columns WHERE table_name = $1", table)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if e := rows.Scan(&name); e != nil {
				return nil, e
			}
			out = append(out, name)
		}
		err = rows.Err()
	}
	return out, err
}

func (t *TableStorage) execDDL(ctx context.Context, stmt string) error {
	if _, err := t.db.ExecContext(ctx, stmt); err != nil {
		return errs.ClassifyStorageError("ensure schema", err)
	}
	return nil
}

// convert coerces one raw CSV/metadata string into the declared type.
func convert(typ, raw string) (any, error) {
	switch typ {
	case "text":
		return raw, nil
	case "int", "bigint":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid %s", raw, typ)
		}
		return n, nil
	case "float":
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid float", raw)
		}
		return f, nil
	case "bool":
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid bool", raw)
		}
		return b, nil
	case "timestamp":
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
			if ts, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
				return ts, nil
			}
		}
		return nil, fmt.Errorf("value %q is not a valid timestamp", raw)
	case "date":
		d, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid date", raw)
		}
		return d, nil
	default:
		return raw, nil
	}
}

// mapRow builds the column values of one CSV record. Missing required columns
// fail with an explicit column/row error.
func (t *TableStorage) mapRow(key model.CollectionKey, fileKey int64, file model.FileIdentity, header []string, fields []string, md model.Metadata, rowNumber int64) ([]any, error) {
	// 按表头名收集全部出现的值（保持出现顺序），Occurrence 选第几个；
	// ragged 行越界补 ""，保证出现次数不因缺列而错位。
	byHeader := map[string][]string{}
	for i, h := range header {
		v := ""
		if i < len(fields) {
			v = fields[i]
		}
		byHeader[h] = append(byHeader[h], v)
	}
	// collection_date：Oracle 方言绑 time.Time（DATE 列的严格类型匹配；
	// 字符串会触发 ORA-01861），其余方言绑字符串（TEXT/DATE 隐式转换）。
	var dateVal any
	if t.cfg.Dialect == "oracle" {
		dateVal = key.Date.Time()
	} else {
		dateVal = key.Date.String()
	}
	values := []any{
		string(key.SourceID),
		dateVal,
		file.Identity(),
		rowNumber,
	}
	if t.cfg.FileKey {
		values = []any{fileKey, rowNumber}
	}
	for _, c := range t.effectiveColumns() {
		var raw string
		var present bool
		switch c.From {
		case SourceMetadata:
			raw, present = md.Get(c.Name)
			if !present {
				raw = ""
			}
		default:
			if vals := byHeader[c.Name]; c.Occurrence < len(vals) {
				raw = vals[c.Occurrence]
				present = true
			}
		}
		if !present || raw == "" {
			if c.Required {
				return nil, fmt.Errorf("row %d: required column %q (from %s %q) is missing", rowNumber, c.Column, c.From, c.Name)
			}
			values = append(values, nil)
			continue
		}
		v, err := convert(c.Type, raw)
		if err != nil {
			return nil, fmt.Errorf("column %q: %v", c.Column, err)
		}
		values = append(values, v)
	}
	return values, nil
}

// resolveFileKey 返回该文件在注册表里的整型键，注册表行缺失或键未分配时
// 就地补齐。键由进程内雪花发号器生成（snowflake.go），不查库、不锁行。
//
// 这一步现在跑在写事务**之外**，是刻意的：登记一条注册表行只需一个语句，
// 而数据行要写满整批（17 列格式 1000 行约 10 个 chunk、每 chunk 两次往返）。
// 旧方案把发号的行锁一直持到整批改写提交，所有并发源排在同一个热点行上。
// 代价是登记可能先于数据落库：写失败会留下一条 records=0 的注册行，重试
// 时按键回查命中、复用同一个键。注册表只被 QueryRows 的 JOIN 读，完成与否
// 由 file-state 判定，不参与去重，所以这条"提前登记"没有正确性影响。
//
// 注册表的 (file_key) 唯一索引是最后防线：万一两个发号器同节点号同毫秒
// 同序号，这里报错而不是让两个文件共用一个键。进程内同一注册表只有一个
// 发号器，所以这一路只可能来自**跨进程**（或测试/人为替换发号器）。不同
// 失败位置的重试语义不同，不要混为一谈：
//   - 登记成功、数据写入失败（或进程崩溃）：注册行已在，重试按键回查命中，
//     沿用原键；数据行按 (file_key, row_number) 幂等重写。
//   - 登记撞三元组 (source_id, collection_date, file_id)：说明同一文件已被
//     别的发号器（或并发的自己）先登记，重试回查命中，复用**胜出者的键**，
//     本次多发的号作废。
//   - 登记撞 (file_key) 唯一索引：说明两个不同文件拿到同一个键，本次无行可
//     回查，整批按约束错误失败；重试会重新发号，这是**唯一**会换键的路径。
//
// 三条路径都不会把数据行挂到另一个文件上；撞键是自愈/可重试事件，不是损坏。
// 若删掉这个唯一索引，INSERT 会静默成功，两个文件共用一个键，JOIN 立刻把
// 两边的数据行叠在一起——所以它不能省。
//
// 每批一次查询，不缓存：批是千行级，一次往返相对千行写入可忽略，而缓存
// 会在跨实例场景下读到过期键。
// resolveFileKey 返回 (键, fresh)。fresh 为真表示本调用刚为这个文件在
// 注册表里立行、且拿到的就是自己新发的雪花键——数据表里不可能已有该键
// 的行，写路径可以省掉逐行探测，直接批量装载。重放、并发登记输家、以及
// 已有登记的文件一律 fresh=false，走"只插缺失行"的幂等直插。
func (t *TableStorage) resolveFileKey(ctx context.Context, batch model.Batch) (int64, bool, error) {
	ft := quoteIdent(t.cfg.Dialect, t.cfg.FileTable)
	p := func(start, n int) string { return placeholders(t.cfg.Dialect, start, n) }
	triple := []any{string(batch.Key.SourceID), t.dateBind(batch.Key.Date), batch.File.Identity()}
	lookup := fmt.Sprintf("SELECT file_key FROM %s WHERE source_id = %s AND collection_date = %s AND file_id = %s", ft, p(1, 1), p(2, 1), p(3, 1))
	// authoritative 回查注册表里那一行的键：并发登记同一文件时，胜出的是
	// 先插进去的那个键，本次多发的号作废（雪花号作废只是留个空洞）。
	// 五方言一律回查——旧版只在 oracle/sqlserver 回查，另三家的
	// insert-if-missing 命中冲突时同样会读到竞争者的行，返回自己多发的键
	// 就会把数据行挂到一个不存在的键上。
	authoritative := func() (int64, error) {
		var found sql.NullInt64
		if err := t.db.QueryRowContext(ctx, lookup, triple...).Scan(&found); err != nil {
			return 0, errs.ClassifyStorageError("file key lookup "+batch.File.Name, err)
		}
		if !found.Valid {
			return 0, errs.Sourcef(errs.ErrStoragePermanent, "file key for %q unresolved", batch.File.Name)
		}
		return found.Int64, nil
	}
	var found sql.NullInt64
	switch err := t.db.QueryRowContext(ctx, lookup, triple...).Scan(&found); {
	case err == nil && found.Valid:
		return found.Int64, false, nil
	case err == nil:
		// 注册表行早于 file_key 列（存量库迁移到这里）：就地补一个雪花键。
		// 旧行的小计数器键与新大键共存无碍——键只要求唯一，不要求连续。
		key, err := t.nextFileKey(ctx)
		if err != nil {
			return 0, false, err
		}
		backfill := fmt.Sprintf("UPDATE %s SET file_key = %s WHERE source_id = %s AND collection_date = %s AND file_id = %s AND file_key IS NULL",
			ft, p(4, 1), p(5, 1), p(6, 1), p(7, 1))
		if _, err := t.db.ExecContext(ctx, backfill, key, triple[0], triple[1], triple[2]); err != nil {
			return 0, false, errs.ClassifyStorageError("file key backfill "+batch.File.Name, err)
		}
		// 两家同时补同一行：UPDATE 的 "AND file_key IS NULL" 让后到的空转，
		// 所以返回行里的实际键，而不是本次可能白发的号。只有实际键等于
		// 本次新发的键，才说明这一行是本次立的。
		got, err := authoritative()
		return got, err == nil && got == key, err
	case errors.Is(err, sql.ErrNoRows):
		key, err := t.nextFileKey(ctx)
		if err != nil {
			return 0, false, err
		}
		// 新文件首次登记：语句按方言选"命中三元组就不插"的写法
		// （见 insertIfMissingStmt）。
		cols := []string{"file_key", "source_id", "collection_date", "file_id", "path", "name", "records", "header", "collected_at"}
		conflict := "source_id, collection_date, file_id"
		insert := insertIfMissingStmt(t.cfg.Dialect, t.cfg.FileTable, cols, conflict)
		// 参数序从 1 开始：file_key 是第一列，与键值在前一致。
		args := []any{key, string(batch.Key.SourceID), t.dateBind(batch.Key.Date), batch.File.Identity(),
			batch.File.Path, batch.File.Name, int64(0), strings.Join(batch.Header, ","), batch.CreatedAt}
		if _, err := t.db.ExecContext(ctx, insert, args...); err != nil {
			return 0, false, errs.ClassifyStorageError("file registry "+batch.File.Name, err)
		}
		// 雪花键在本进程内唯一，注册表里读到它只可能是本次插入的那行；
		// 读到别的键说明并发登记输给了先到者。
		got, err := authoritative()
		return got, err == nil && got == key, err
	default:
		return 0, false, errs.ClassifyStorageError("file lookup "+batch.File.Name, err)
	}
}

// insertIfMissingStmt 生成"命中冲突键就不插"的语句。各家写法互斥：
// oracle/sqlserver 只能 MERGE，mysql 无 ON CONFLICT（用 INSERT IGNORE），
// sqlite/postgres 用 ON CONFLICT DO NOTHING。没有哪一家的写法能通吃，
// 所以这一处按方言分支，写错的代价是"整条写入路径报语法错"。
func insertIfMissingStmt(dialect, rawTable string, cols []string, conflict string) string {
	q := quoteIdent(dialect, rawTable)
	switch dialect {
	case "oracle", "sqlserver":
		return mergeUpsert(dialect, rawTable, cols, conflict)
	case "mysql":
		return fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES (%s)",
			q, strings.Join(cols, ", "), placeholders(dialect, 1, len(cols)))
	default:
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO NOTHING",
			q, strings.Join(cols, ", "), placeholders(dialect, 1, len(cols)), conflict)
	}
}

// headerCheck 把解码表头与声明列双向比对，计数单位是"表头名的出现次数"：
// 同一 name 声明 k 次（重名表头用 occurrence 区分）才允许它在表头出现 k 次。
// 取值口径与 mapRow 一致——两边都用表头/声明的原文，不做 TrimSpace，所以带
// 空格的表头确实取不到值，这里就如实报失配。
//
// 两个方向的代价不同，处置也不同：
//   - 表头有、声明没有：这些字段的值正在被丢弃。默认（header_check =
//     "strict"）判整个文件失败，且在任何 DB 往返之前失败——文件不落半截、
//     不登记完成，采集按 catchup_days 重排，声明补齐后自动补采。写成
//     "warn" 只告警。auto_columns 的契约本就是"按表头增量建列"，extra_rows
//     开着的格式未声明字段已整行进 row_values：这两种形态值不丢，恒只告警。
//   - 声明有、表头没有：没有数据可丢（取空值），恒为告警；同一格式存在代际
//     差异时用列级 optional 豁免。
func (t *TableStorage) headerCheck(batch model.Batch) error {
	if len(t.cfg.Columns) == 0 || len(batch.Header) == 0 {
		return nil
	}
	declared := map[string]int{}
	for _, c := range t.cfg.Columns {
		if c.From == SourceMetadata {
			continue
		}
		declared[c.Name]++
	}
	seen := map[string]int{}
	var order []string
	for _, h := range batch.Header {
		if strings.TrimSpace(h) == "" {
			continue // 空表头不可声明，跳过（与 deriveColumns 一致）
		}
		if seen[h] == 0 {
			order = append(order, h)
		}
		seen[h]++
	}
	var extras []string
	for _, name := range order {
		if seen[name] > declared[name] {
			extras = append(extras, name)
		}
	}
	blocking := t.cfg.HeaderCheck == "strict" && !t.cfg.AutoColumns && !t.cfg.ExtraRows
	if len(extras) > 0 {
		for _, name := range extras {
			if t.warnOnce("extra", name) {
				slog.Warn("header field is not declared, its value is dropped",
					"table", t.cfg.Table, "file", batch.File.Name, "header_field", name,
					"blocked", blocking)
			}
		}
		if blocking {
			return errs.Sourcef(errs.ErrStoragePermanent,
				"header check: file %q has %d undeclared header field(s) for table %q (%s); their values would be lost — declare them, or set header_check = \"warn\" / extra_rows = true",
				batch.File.Name, len(extras), t.cfg.Table, strings.Join(extras, ", "))
		}
	}
	for i := range t.cfg.Columns {
		c := &t.cfg.Columns[i]
		if c.From == SourceMetadata || c.Optional {
			continue
		}
		if seen[c.Name] > c.Occurrence {
			continue
		}
		if t.warnOnce("missing", c.Column) {
			slog.Warn("declared column has no matching header field, value stored empty",
				"table", t.cfg.Table, "file", batch.File.Name,
				"column", c.Column, "header_field", c.Name)
		}
	}
	return nil
}

// warnOnce 同一 (方向, 字段) 只记一条：长跑进程按文件刷屏没有信息量。
// 写路径按源串行，无需加锁（与 driftWarned 的既有约定一致）。
func (t *TableStorage) warnOnce(kind, key string) bool {
	if t.driftWarned == nil {
		t.driftWarned = map[string]bool{}
	}
	full := kind + "\x00" + key
	if t.driftWarned[full] {
		return false
	}
	t.driftWarned[full] = true
	return true
}

// Write implements model.Storage.
func (t *TableStorage) Write(ctx context.Context, batch model.Batch) error {
	return t.write(ctx, batch, true)
}

// write 的 allowFast 只服务内部回退：SQL Server 上首次装载走 TDS 批量，
// 重放/已登记文件走"只插缺失行"的直插；两者撞唯一键（只可能来自并发
// 重复处理同一文件）时禁用快速路径，按幂等 MERGE 重放一次。
func (t *TableStorage) write(ctx context.Context, batch model.Batch, allowFast bool) error {
	if err := t.headerCheck(batch); err != nil {
		return err
	}
	if len(batch.Records) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 连接先行：惰性库在首个批次才打开，派生列后的补建表必须持有 db。
	if t.db == nil {
		if err := t.EnsureConnected(ctx); err != nil {
			return err
		}
	}
	// 自动字段映射：以解码表头建列；后续文件出现新表头字段（或既有字段
	// 的新一次出现）时增量加列，同名文件恒落同表、同名字段恒落同列。
	if t.cfg.AutoColumns && len(t.cfg.Columns) == 0 && len(batch.Header) > 0 {
		if err := t.evolveAutoColumns(ctx, batch.Header); err != nil {
			return err
		}
	}
	// file_key 骨架：整批共用一个键，逐行不再重复 file_id 文本。
	// 发号与登记在事务**之外**（resolveFileKey 自己管往返）：登记只有一条
	// 语句，数据行才是整批改写，两者放进同一个事务会让发号/登记成为跨源
	// 的串行点——现场 15 个会话 LCK_M_X 等同一个计数器行，等待 8–13 秒。
	var fileKey int64
	fresh := false
	if t.cfg.FileKey {
		fk, f, kerr := t.resolveFileKey(ctx, batch)
		if kerr != nil {
			return kerr
		}
		fileKey = fk
		fresh = f
	}
	// SQL Server 的两种免探测写：新发的键在数据表里绝无既有行，走 TDS
	// 批量装载（实测 1000 行 × 20 列 263ms→32ms、× 70 列 822ms→41ms）；
	// 重放/已登记文件只插缺失行，NOT EXISTS 的 S 锁语句级释放，不像 MERGE
	// 那样把探测 U 锁持有到提交。其它方言未做同口径测量，保持原语义。
	fast := allowFast && t.cfg.Dialect == "sqlserver"
	bulkFresh := fast && fresh
	insertMissing := fast && !bulkFresh
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return errs.ClassifyStorageError("begin", err)
	}
	defer tx.Rollback()

	// File registry: one row per file (written with the first batch). file_key
	// 模式下这一步改由 resolveFileKey 逐批负责（批批要有键，注册表行缺则补）。
	if t.cfg.FileTable != "" && !t.cfg.FileKey && batch.Sequence == 1 {
		header := strings.Join(batch.Header, ",")
		fCols := []string{"source_id", "collection_date", "file_id", "path", "name", "records", "header", "collected_at"}
		stmt := insertIfMissingStmt(t.cfg.Dialect, t.cfg.FileTable, fCols, "source_id, collection_date, file_id")
		if _, err := tx.ExecContext(ctx, stmt,
			string(batch.Key.SourceID), t.dateBind(batch.Key.Date), batch.File.Identity(),
			batch.File.Path, batch.File.Name, 0, header, batch.CreatedAt); err != nil {
			return errs.ClassifyStorageError("file registry "+batch.File.Name, err)
		}
	}

	cols := []string{"source_id", "collection_date", "file_id", "row_number"}
	if t.cfg.FileKey {
		cols = []string{"file_key", "row_number"}
	}
	// rawCols 供批量装载用：CopyIn 自己给对象名加引号，只接受裸列名。
	rawCols := append([]string(nil), cols...)
	// 表头与声明列的双向比对已在 Write 入口做过（headerCheck）：未声明的表头
	// 字段默认阻断，auto_columns / extra_rows 形态下降级为告警。
	for _, c := range t.effectiveColumns() {
		cols = append(cols, quoteIdent(t.cfg.Dialect, c.Column))
		rawCols = append(rawCols, c.Column)
	}
	if t.cfg.ExtraRows {
		cols = append(cols, "row_values")
		rawCols = append(rawCols, "row_values")
	}
	conflict := "source_id, collection_date, file_id, row_number"
	if t.cfg.FileKey {
		conflict = "file_key, row_number"
	}
	// 先把全部行映射成绑定值：任何一行映射失败（必填列缺失等）在任何
	// DB 往返发生之前就失败整个文件——与旧行为（事务回滚）等价，且更省。
	rowValues := make([][]any, 0, len(batch.Records))
	for i := range batch.Records {
		rec := &batch.Records[i]
		values, merr := t.mapRow(batch.Key, fileKey, batch.File, batch.Header, rec.Fields, batch.Metadata, rec.RowNumber)
		if merr != nil {
			return errs.Sourcef(errs.ErrStoragePermanent, "file %q row %d: %v", batch.File.Name, rec.RowNumber, merr)
		}
		if t.cfg.ExtraRows {
			raw, _ := json.Marshal(rec.Fields)
			values = append(values, string(raw))
		}
		rowValues = append(rowValues, values)
	}
	if bulkFresh {
		if berr := t.bulkInsert(ctx, tx, rawCols, rowValues, batch.File.Name); berr != nil {
			// 回滚后禁用快速路径重来：注册行已落，resolve 必然非 fresh，
			// 走幂等 MERGE。撞键只可能来自并发重复处理同一文件，不能让
			// 一次正常重采集判成永久失败。
			_ = tx.Rollback()
			if errors.Is(berr, errs.ErrStorageConstraint) {
				return t.write(ctx, batch, false)
			}
			return berr
		}
	} else if insertMissing {
		// 重放/已登记：只插缺失行。NOT EXISTS 探测在 READ COMMITTED 下
		// 只取语句级 S 锁，不像 MERGE 的 U 锁那样持有到提交，避免多文件
		// 并发重放时的锁环。并发重复处理同一文件撞唯一键时回退 MERGE。
		n := chunkRowsFor(t.cfg.Dialect, len(cols))
		for start := 0; start < len(rowValues); start += n {
			end := start + n
			if end > len(rowValues) {
				end = len(rowValues)
			}
			chunk := rowValues[start:end]
			stmt := buildInsertMissing(t.cfg.Table, cols, conflict, len(chunk))
			if err := t.execChunk(ctx, tx, stmt, chunk, cols, batch); err != nil {
				_ = tx.Rollback()
				if errors.Is(err, errs.ErrStorageConstraint) {
					return t.write(ctx, batch, false)
				}
				return err
			}
		}
	} else {
		// 多行写入：每 chunk 一次网络往返（逐行 ExecContext 在局域网
		// Oracle 上是 1000 行 = 1000 次 RTT——切真库后的最大单项开销）。
		// 幂等语义逐方言保留（见 buildMultiRow）。
		n := chunkRowsFor(t.cfg.Dialect, len(cols))
		for start := 0; start < len(rowValues); start += n {
			end := start + n
			if end > len(rowValues) {
				end = len(rowValues)
			}
			chunk := rowValues[start:end]
			stmt := buildMultiRow(t.cfg.Dialect, t.cfg.Table, cols, conflict, len(chunk))
			if err := t.execChunk(ctx, tx, stmt, chunk, cols, batch); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return errs.ClassifyStorageError("commit", err)
	}
	return nil
}

// bulkInsert 用 TDS 批量装载写整批。只用于 SQL Server 的 fresh 键首写：
// 新键在数据表里没有既有行，不需要逐行冲突探测。KeepNulls 保住 CSV 里的
// 空值语义（目标列无默认值，但显式声明避免依赖默认行为）。RowsPerBatch
// 给优化器准确基数：不传时 16 并发整轮内存授予等待峰值 40 路，传了即消失。
func (t *TableStorage) bulkInsert(ctx context.Context, tx *sql.Tx, cols []string, rows [][]any, fileName string) error {
	opts := mssql.BulkOptions{KeepNulls: true, RowsPerBatch: len(rows)}
	stmt, err := tx.PrepareContext(ctx, mssql.CopyIn(t.cfg.Table, opts, cols...))
	if err != nil {
		return errs.ClassifyStorageError("prepare bulk insert "+fileName, err)
	}
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row...); err != nil {
			stmt.Close()
			return errs.ClassifyStorageError("write rows "+fileName, err)
		}
	}
	if _, err := stmt.ExecContext(ctx); err != nil {
		stmt.Close()
		return errs.ClassifyStorageError("flush bulk insert "+fileName, err)
	}
	if err := stmt.Close(); err != nil {
		return errs.ClassifyStorageError("close bulk insert "+fileName, err)
	}
	return nil
}

// execChunk 绑定并执行一条多行写入语句。每次 Prepare 只服务一个 chunk，
// 参数个数按行优先展开（与 buildMultiRow 的占位符顺序一致）。
func (t *TableStorage) execChunk(ctx context.Context, tx *sql.Tx, stmt string, chunk [][]any, cols []string, batch model.Batch) error {
	insertStmt, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		return errs.ClassifyStorageError("prepare insert", err)
	}
	args := make([]any, 0, len(chunk)*len(cols))
	for _, v := range chunk {
		args = append(args, v...)
	}
	if _, err := insertStmt.ExecContext(ctx, args...); err != nil {
		insertStmt.Close()
		return errs.ClassifyStorageError("write rows "+batch.File.Name, err)
	}
	if err := insertStmt.Close(); err != nil {
		return errs.ClassifyStorageError("close insert", err)
	}
	return nil
}

// QueryRows implements RowsQuery.
func (t *TableStorage) QueryRows(ctx context.Context, sourceID, date string, limit, offset int, filters map[string]string) (RowsPage, error) {
	page := RowsPage{Columns: []string{}, Rows: [][]interface{}{}}
	if t.db == nil {
		// lazy_connect 模式：读路径同样按需建连，重启后无需一次写入来“唤醒”。
		if !t.lazy {
			return page, errs.ClassifyStorageError("closed", sql.ErrConnDone)
		}
		if err := t.EnsureConnected(ctx); err != nil {
			return page, err
		}
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	declared := map[string]bool{}
	for _, c := range t.effectiveColumns() {
		declared[strings.ToLower(c.Column)] = true
	}
	// file_key 模式下三个业务键列住在注册表里，读侧 JOIN 回来。对控制台的
	// 列名契约不变：source_id/collection_date/file_id 照旧可选、可滤、可显示。
	qual := func(name string) string {
		q := quoteIdent(t.cfg.Dialect, name)
		if !t.cfg.FileKey {
			return q
		}
		switch name {
		case "source_id", "collection_date", "file_id":
			// 声明列恰好取了同名（自动字段映射按表头命名，可能就叫
			// source_id）：这时它是数据列，归 d.，不能被注册表同名列顶掉。
			if !declared[strings.ToLower(name)] {
				return "f." + q
			}
		}
		return "d." + q
	}
	// sourceID/date 留空表示该维度不过滤：控制台的跨批次查询依赖这一点。
	where, args, err := readFilterClauses(t.cfg.Dialect, qual, t.dateFilterBind, sourceID, date, filters, declared)
	if err != nil {
		return page, err
	}
	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ")
	}
	cols := append([]string{"source_id", "collection_date", "file_id", "row_number"}, func() []string {
		out := make([]string, 0, len(t.cfg.Columns))
		for _, c := range t.effectiveColumns() {
			out = append(out, c.Column)
		}
		return out
	}()...)
	quoted := make([]string, 0, len(cols))
	for _, c := range cols {
		quoted = append(quoted, qual(c))
	}
	tbl := quoteIdent(t.cfg.Dialect, t.cfg.Table)
	from, orderBy := tbl, "collection_date, file_id, row_number"
	if t.cfg.FileKey {
		from = fmt.Sprintf("%s d JOIN %s f ON f.file_key = d.file_key", tbl, quoteIdent(t.cfg.Dialect, t.cfg.FileTable))
		orderBy = "f.collection_date, f.file_id, d.row_number"
	}
	query := fmt.Sprintf(
		"SELECT %s FROM %s %s ORDER BY %s LIMIT %d OFFSET %d",
		strings.Join(quoted, ", "), from, w, orderBy, limit, offset)
	switch t.cfg.Dialect {
	case "oracle":
		// Oracle 11g 兼容分页（ROWNUM 包装；绑定参数都在内层，边界用
		// 已校验的整数字面量，无注入面）。11g 无 OFFSET/FETCH 语法。
		query = fmt.Sprintf(
			"SELECT * FROM (SELECT q.*, ROWNUM rn FROM (SELECT %s FROM %s %s ORDER BY %s) q WHERE ROWNUM <= %d) WHERE rn > %d",
			strings.Join(quoted, ", "), from, w, orderBy, offset+limit, offset)
	case "sqlserver":
		// SQL Server 2012+ OFFSET/FETCH。
		query = fmt.Sprintf(
			"SELECT %s FROM %s %s ORDER BY %s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY",
			strings.Join(quoted, ", "), from, w, orderBy, offset, limit)
	}
	// Total 先算：页面 SELECT 会在单连接池（SQLite）里占用唯一连接，
	// 结果集未关就发 COUNT 会同池自锁（无限连接池掩盖了这一顺序缺陷）。
	if err := t.db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT COUNT(*) FROM %s %s", from, w), args...).Scan(&page.Total); err != nil {
		return page, errs.ClassifyStorageError("count rows", err)
	}
	rows, err := t.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, errs.ClassifyStorageError("query rows", err)
	}
	defer rows.Close()
	page.Columns = cols
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return page, errs.ClassifyStorageError("scan rows", err)
		}
		page.Rows = append(page.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return page, errs.ClassifyStorageError("iterate rows", err)
	}
	return page, nil
}

// readFilterClauses 组装读侧的过滤条件。占位符必须逐方言生成：写死 ? 在
// sqlserver（@pN）与 oracle（:N）上不是合法绑定，实测 mssql 报
// "Incorrect syntax near '?'"。序号随 args 长度递增，五方言共用一条规则。
func readFilterClauses(dialect string, qual func(string) string, dateBind func(string) any,
	sourceID, date string, filters map[string]string, declared map[string]bool) ([]string, []any, error) {
	where := []string{}
	args := []any{}
	bind := func() string { return placeholders(dialect, len(args)+1, 1) }
	if sourceID != "" {
		where = append(where, qual("source_id")+" = "+bind())
		args = append(args, sourceID)
	}
	if date != "" {
		where = append(where, qual("collection_date")+" = "+bind())
		args = append(args, dateBind(date))
	}
	filterCols := make([]string, 0, len(filters))
	for c := range filters {
		filterCols = append(filterCols, c)
	}
	sort.Strings(filterCols)
	for _, c := range filterCols {
		if !declared[strings.ToLower(c)] {
			return nil, nil, errs.Sourcef(errs.ErrInvalidConfig, "filter column %q is not a declared column", c)
		}
		where = append(where, qual(c)+" = "+bind())
		args = append(args, filters[c])
	}
	return where, args, nil
}

// Stats implements the storage insight view: connectivity plus per-table row
// counts. A down database reports connected=false instead of failing.
func (t *TableStorage) Stats(ctx context.Context) (StorageStats, error) {
	out := StorageStats{Connected: false}
	if t.db == nil {
		if !t.lazy {
			return out, nil
		}
		if err := t.EnsureConnected(ctx); err != nil {
			return out, nil // down database: honest offline, not an error
		}
	}
	if err := t.db.PingContext(ctx); err != nil {
		return out, nil
	}
	out.Connected = true
	for _, name := range []string{t.cfg.Table, t.cfg.FileTable} {
		if name == "" {
			continue
		}
		var n int64
		if err := t.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+quoteIdent(t.cfg.Dialect, name)).Scan(&n); err == nil {
			out.Tables = append(out.Tables, TableStat{Table: name, Rows: n})
		}
	}
	return out, nil
}

func (t *TableStorage) Close() error {
	if t.closeMe == nil {
		return nil
	}
	err := t.closeMe()
	t.db = nil
	t.closeMe = nil
	return err
}

var _ model.Storage = (*TableStorage)(nil)
var _ RowsQuery = (*TableStorage)(nil)

// TableRowsQueryKey lets the console bridge discover the typed sink's read
// side and expose it as the "rows" console query. Absent when no typed sink
// is active — the bridge skips registration in that case.
var TableRowsQueryKey = runtime.NewKey[RowsQuery]("console.rows.query")

// ensureSQLiteDir creates the parent directory of a sqlite file DSN, so a
// fresh deployment does not fail on a missing state directory.
// sqliteDSN 为 SQLite DSN 追加 busy_timeout：多源共库（多机台写入同一
// 文件）时，并发 DDL/写库等待锁而不是立即报 SQLITE_BUSY。
func sqliteDSN(cfg TableConfig) string {
	if cfg.Driver != "sqlite" {
		return cfg.DSN
	}
	sep := "?"
	if strings.Contains(cfg.DSN, "?") {
		sep = "&"
	}
	return cfg.DSN + sep + "_pragma=busy_timeout(5000)"
}

func ensureSQLiteDir(cfg TableConfig) {
	if cfg.Dialect != "sqlite" {
		return
	}
	if dir := filepath.Dir(cfg.DSN); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
}

// dateBind 按方言绑定业务日期：Oracle 用 time.Time（DATE 列严格类型），
// 其余方言用 ISO 字符串。
func (t *TableStorage) dateBind(d model.CollectionDate) any {
	if t.cfg.Dialect == "oracle" {
		return d.Time()
	}
	return d.String()
}

// dateFilterBind 是读侧的日期过滤值：与 dateBind 同规则，否则写入用
// time.Time、过滤用字符串，oracle 的 DATE 列两边比不上。
func (t *TableStorage) dateFilterBind(s string) any {
	if t.cfg.Dialect != "oracle" {
		return s
	}
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		return s // 非 ISO 日期：原样交给数据库，失败即报错，不做静默猜测
	}
	return d.Time()
}

// mergeUpsert 生成 Oracle / SQL Server 的 MERGE 幂等写入。三家差异都在
// 这里收口：绑定符（oracle :N / sqlserver @pN）、FROM DUAL（oracle 必需、
// sqlserver 禁止）、语句终止符（T-SQL 的 MERGE 必须以分号结束）。
func mergeUpsert(dialect, table string, cols []string, conflict string) string {
	dual := ""
	if dialect == "oracle" {
		dual = " FROM DUAL"
	}
	semi := ""
	if dialect == "sqlserver" {
		semi = ";"
	}
	var selected, insertCols, insertVals, on []string
	keySet := map[string]bool{}
	for _, k := range strings.Split(conflict, ", ") {
		keySet[strings.ToLower(k)] = true
	}
	for i, c := range cols {
		alias := fmt.Sprintf("c%d", i)
		selected = append(selected, fmt.Sprintf("%s AS %s", placeholders(dialect, i+1, 1), alias))
		insertCols = append(insertCols, c)
		insertVals = append(insertVals, "src."+alias)
		if keySet[strings.ToLower(strings.Trim(c, `"`))] {
			on = append(on, fmt.Sprintf("dst.%s = src.%s", c, alias))
		}
	}
	return fmt.Sprintf("MERGE INTO %s dst USING (SELECT %s%s) src ON (%s) WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)%s",
		quoteIdent(dialect, table), strings.Join(selected, ", "), dual,
		strings.Join(on, " AND "), strings.Join(insertCols, ", "), strings.Join(insertVals, ", "), semi)
}
