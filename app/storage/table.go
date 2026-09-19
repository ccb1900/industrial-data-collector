// Typed relational sink: maps CSV data columns and metadata keys onto real
// database columns. No JSON blobs — every column is typed and queryable.
// The idempotency backbone (source_id, collection_date, file_id, row_number)
// is identical to the generic records sink, so replay/retry semantics are
// unchanged. The CSV header of each collected file is stored once in a file
// registry table as plain TEXT.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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
	From     ColumnSource `toml:"from"`
	Name     string       `toml:"name"`     // source key: CSV header field or metadata key
	Column   string       `toml:"column"`   // database column name
	Type     string       `toml:"type"`     // text|int|bigint|float|bool|timestamp|date
	Required bool         `toml:"required"` // missing value fails the file
}

// TableConfig configures the typed relational sink.
type TableConfig struct {
	Driver    string
	DSN       string
	Dialect   string
	Table     string
	FileTable string          // optional per-file registry (header as TEXT)
	Columns   []ColumnMapping // typed columns
	ExtraRows bool            // store unmapped CSV fields into row_values TEXT
	Exposer   bool            // expose the RowsQuery capability for consoles
	Lazy      bool            // defer connection to first use
	// AutoColumns 自动字段映射：Columns 未声明时，首个批次的解码表头
	// 自动建列（全部 TEXT，列名即表头文本）。声明了 Columns 时本开关无效。
	AutoColumns bool
}

func (c *TableConfig) Validate() error {
	if c.Driver == "" || c.DSN == "" {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage driver/dsn required")
	}
	c.Dialect = strings.ToLower(c.Dialect)
	switch c.Dialect {
	case "postgres", "mysql", "sqlite", "oracle":
	default:
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage dialect %q not supported (postgres/mysql/sqlite/oracle)", c.Dialect)
	}
	if c.Table == "" {
		c.Table = "records"
	}
	if !tableNamePattern.MatchString(c.Table) {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage table %q is not a simple identifier", c.Table)
	}
	if c.FileTable != "" && !tableNamePattern.MatchString(c.FileTable) {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage file_table %q is not a simple identifier", c.FileTable)
	}
	if len(c.Columns) == 0 && !c.AutoColumns {
		return errs.Sourcef(errs.ErrInvalidConfig, "table storage %q: no columns declared (set columns or auto_columns)", c.Table)
	}
	seen := map[string]bool{}
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
		if !tableNamePattern.MatchString(col.Column) {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q is not a simple identifier", col.Column)
		}
		if seen[col.Column] {
			return errs.Sourcef(errs.ErrInvalidConfig, "column %q declared twice", col.Column)
		}
		seen[col.Column] = true
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

	// 自动字段映射：columns 未声明时，首个批次用解码后的表头建列。
	// effectiveColumns 是全部读路径的列来源；原子指针保证控制台查询
	// 与采集写入并发时的可见性与无竞争。
	autoOnce    sync.Once
	autoCols    atomic.Pointer[[]ColumnMapping]
	autoSchema  atomic.Bool
	driftWarned map[string]bool // 写路径串行访问
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
// 名精确取值），Column 即表头文本（quoteIdent 负责安全引用）。空白列跳
// 过；重复表头只声明一次（按名取值本就取最后一次出现）。
func deriveColumns(header []string) []ColumnMapping {
	cols := make([]ColumnMapping, 0, len(header))
	seen := map[string]bool{}
	for _, raw := range header {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
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
		seen[name] = true
		cols = append(cols, ColumnMapping{From: SourceCSV, Name: raw, Column: name, Type: "text"})
	}
	return cols
}

// OpenTable opens the typed sink eagerly.
func OpenTable(ctx context.Context, cfg TableConfig) (*TableStorage, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ensureSQLiteDir(cfg)
	db, err := sql.Open(cfg.Driver, sqliteDSN(cfg))
	if err != nil {
		return nil, errs.ClassifyStorageError("open", err)
	}
	if cfg.Dialect == "sqlite" {
		// SQLite 单写者：进程内串行化避免 "database is locked"，跨进程由
		// busy_timeout 兜底；:memory: 的池化多连接各自独立库，也由此根治。
		db.SetMaxOpenConns(1)
	}
	if err != nil {
		return nil, errs.ClassifyStorageError("open", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errs.ClassifyStorageError("ping", err)
	}
	t := &TableStorage{db: db, cfg: cfg, closeMe: db.Close}
	if err := t.ensureSchema(ctx); err != nil {
		_ = db.Close()
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
	db, err := sql.Open(t.cfg.Driver, sqliteDSN(t.cfg))
	if err != nil {
		return errs.ClassifyStorageError("open", err)
	}
	if t.cfg.Dialect == "sqlite" {
		// 惰性路径与 OpenTable 同约定：进程内单连接，跨进程 busy_timeout。
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return errs.ClassifyStorageError("ping", err)
	}
	t.db = db
	t.closeMe = db.Close
	return t.ensureSchema(ctx)
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
	default: // sqlite
		return map[string]string{"text": "TEXT", "int": "INTEGER", "bigint": "INTEGER", "float": "REAL", "bool": "INTEGER", "timestamp": "TEXT", "date": "TEXT"}
	}
}

func (t *TableStorage) ensureSchema(ctx context.Context) error {
	dt := dialectTypes(t.cfg.Dialect)
	oracle := t.cfg.Dialect == "oracle"
	// Data table: idempotency backbone + declared columns. Oracle 的键列
	// 用定长类型：PK 索引键超长会 ORA-01450，不能用 VARCHAR2(4000)。
	cols := []string{
		"source_id " + dt["text"] + " NOT NULL",
		"collection_date " + dt["date"] + " NOT NULL",
		"file_id " + dt["text"] + " NOT NULL",
		"row_number " + dt["bigint"] + " NOT NULL",
	}
	if oracle {
		cols = []string{
			"source_id VARCHAR2(255) NOT NULL",
			"collection_date DATE NOT NULL",
			"file_id VARCHAR2(255) NOT NULL",
			"row_number NUMBER(19) NOT NULL",
		}
	}
	for _, c := range t.effectiveColumns() {
		cols = append(cols, quoteIdent(t.cfg.Dialect, c.Column)+" "+dt[c.Type])
	}
	if t.cfg.ExtraRows {
		cols = append(cols, "row_values "+dt["text"])
	}
	pk := "PRIMARY KEY (source_id, collection_date, file_id, row_number)"
	if oracle {
		// Oracle 无 IF NOT EXISTS：查 user_tables 后按需建表。
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
	for _, c := range t.effectiveColumns() {
		if have[strings.ToLower(c.Column)] {
			continue
		}
		fmt.Printf("[DBG] ensureSchema ALTER add %q table=%q\n", c.Column, t.cfg.Table)
		ddl := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", quoteIdent(t.cfg.Dialect, t.cfg.Table), quoteIdent(t.cfg.Dialect, c.Column), dt[c.Type])
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
				fileDDL = fmt.Sprintf("CREATE TABLE %s (source_id VARCHAR2(255) NOT NULL, collection_date DATE NOT NULL, file_id VARCHAR2(255) NOT NULL, path VARCHAR2(1000), name VARCHAR2(255), records NUMBER(19), header VARCHAR2(1), collected_at TIMESTAMP, PRIMARY KEY (source_id, collection_date, file_id))", ft)
			} else {
				fileDDL = ""
			}
		}
		if fileDDL != "" {
			if err := t.execDDL(ctx, fileDDL); err != nil {
				return err
			}
		}
	}
	return nil
}

// tableExists reports whether the table already exists (Oracle dialect).
func (t *TableStorage) tableExists(ctx context.Context, table string) bool {
	var cnt int
	if err := t.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM user_tables WHERE table_name = UPPER(:1)", table).Scan(&cnt); err != nil {
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
	default: // postgres / mysql: information_schema
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
func (t *TableStorage) mapRow(key model.CollectionKey, file model.FileIdentity, header []string, fields []string, md model.Metadata, rowNumber int64) ([]any, error) {
	byHeader := map[string]string{}
	for i, h := range header {
		if i < len(fields) {
			byHeader[h] = fields[i]
		}
	}
	values := []any{
		string(key.SourceID),
		key.Date.String(),
		file.Identity(),
		rowNumber,
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
			raw, present = byHeader[c.Name]
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

// Write implements model.Storage.
func (t *TableStorage) Write(ctx context.Context, batch model.Batch) error {
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
	// 自动字段映射：以首个批次的解码表头建列。派生后补一次 ensureSchema
	//（幂等：CREATE IF NOT EXISTS + 增量 ALTER 补齐新列）。
	if t.cfg.AutoColumns && len(t.cfg.Columns) == 0 && len(batch.Header) > 0 {
		t.autoOnce.Do(func() {
			cols := deriveColumns(batch.Header)
			t.autoCols.Store(&cols)
		})
		if t.autoCols.Load() != nil && !t.autoSchema.Swap(true) {
			if err := t.ensureSchema(ctx); err != nil {
				// 建列失败必须回退闸门：否则一次瞬时错误（如 SQLITE_BUSY
				// 超时）就让后续所有批次永久引用不存在的列。
				t.autoSchema.Store(false)
				return err
			}
		}
	}
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return errs.ClassifyStorageError("begin", err)
	}
	defer tx.Rollback()

	// File registry: one row per file (written with the first batch).
	if t.cfg.FileTable != "" && batch.Sequence == 1 {
		header := strings.Join(batch.Header, ",")
		ft := quoteIdent(t.cfg.Dialect, t.cfg.FileTable)
		stmt := fmt.Sprintf("INSERT INTO %s (source_id, collection_date, file_id, path, name, records, header, collected_at) VALUES (%s) ON CONFLICT (source_id, collection_date, file_id) DO NOTHING",
			ft, placeholders(t.cfg.Dialect, 1, 8))
		if _, err := tx.ExecContext(ctx, stmt,
			string(batch.Key.SourceID), batch.Key.Date.String(), batch.File.Identity(),
			batch.File.Path, batch.File.Name, 0, header, batch.CreatedAt); err != nil {
			return errs.ClassifyStorageError("file registry "+batch.File.Name, err)
		}
	}

	cols := []string{"source_id", "collection_date", "file_id", "row_number"}
	declared := map[string]bool{}
	for _, c := range t.effectiveColumns() {
		cols = append(cols, quoteIdent(t.cfg.Dialect, c.Column))
		declared[c.Name] = true
	}
	if t.cfg.AutoColumns {
		// 自动映射模式下列集在首批评次固定：后续文件新增的表头字段无法
		// 落列，必须告警而不是静默丢弃。
		for _, h := range batch.Header {
			name := strings.TrimSpace(h)
			if name == "" || declared[name] {
				continue
			}
			if t.driftWarned == nil {
				t.driftWarned = map[string]bool{}
			}
			if !t.driftWarned[name] {
				t.driftWarned[name] = true
				slog.Warn("auto-mapped source grew a new header column after first batch; value ignored (declare columns or re-create table)",
					"table", t.cfg.Table, "column", name)
			}
		}
	}
	if t.cfg.ExtraRows {
		cols = append(cols, "row_values")
	}
	placeholdersList := placeholders(t.cfg.Dialect, 1, len(cols))
	conflict := "source_id, collection_date, file_id, row_number"
	var stmt string
	switch t.cfg.Dialect {
	case "mysql":
		stmt = fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES (%s)", quoteIdent(t.cfg.Dialect, t.cfg.Table), strings.Join(cols, ", "), placeholdersList)
	default:
		stmt = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO NOTHING", quoteIdent(t.cfg.Dialect, t.cfg.Table), strings.Join(cols, ", "), placeholdersList, conflict)
	}
	// 语句整批预编译一次：database/sql 的 ExecContext 逐行调用会重复
	// prepare，batch_size=1000 的 Oracle 批次就是 1000 次 prepare+execute；
	// 预编译后循环内只做参数绑定与执行。
	insertStmt, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		return errs.ClassifyStorageError("prepare insert", err)
	}
	defer insertStmt.Close()
	for i := range batch.Records {
		rec := &batch.Records[i]
		values, merr := t.mapRow(batch.Key, batch.File, batch.Header, rec.Fields, batch.Metadata, rec.RowNumber)
		if merr != nil {
			return errs.Sourcef(errs.ErrStoragePermanent, "file %q row %d: %v", batch.File.Name, rec.RowNumber, merr)
		}
		if t.cfg.ExtraRows {
			raw, _ := json.Marshal(rec.Fields)
			values = append(values, string(raw))
		}
		if _, err := insertStmt.ExecContext(ctx, values...); err != nil {
			return errs.ClassifyStorageError("write row "+batch.File.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return errs.ClassifyStorageError("commit", err)
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
	// sourceID/date 留空表示该维度不过滤：控制台的跨批次查询依赖这一点。
	where := []string{}
	args := []any{}
	if sourceID != "" {
		where = append(where, "source_id = ?")
		args = append(args, sourceID)
	}
	if date != "" {
		where = append(where, "collection_date = ?")
		args = append(args, date)
	}
	filterCols := make([]string, 0, len(filters))
	for c := range filters {
		filterCols = append(filterCols, c)
	}
	sort.Strings(filterCols)
	for _, c := range filterCols {
		if !declared[strings.ToLower(c)] {
			return page, errs.Sourcef(errs.ErrInvalidConfig, "filter column %q is not a declared column", c)
		}
		where = append(where, quoteIdent(t.cfg.Dialect, c)+" = ?")
		args = append(args, filters[c])
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
		quoted = append(quoted, quoteIdent(t.cfg.Dialect, c))
	}
	tbl := quoteIdent(t.cfg.Dialect, t.cfg.Table)
	query := fmt.Sprintf(
		"SELECT %s FROM %s %s ORDER BY collection_date, file_id, row_number LIMIT %d OFFSET %d",
		strings.Join(quoted, ", "), tbl, w, limit, offset)
	if t.cfg.Dialect == "oracle" {
		// Oracle 11g 兼容分页（ROWNUM 包装；绑定参数都在内层，边界用
		// 已校验的整数字面量，无注入面）。11g 无 OFFSET/FETCH 语法。
		query = fmt.Sprintf(
			"SELECT * FROM (SELECT q.*, ROWNUM rn FROM (SELECT %s FROM %s %s ORDER BY collection_date, file_id, row_number) q WHERE ROWNUM <= %d) WHERE rn > %d",
			strings.Join(quoted, ", "), tbl, w, offset+limit, offset)
	}
	// Total 先算：页面 SELECT 会在单连接池（SQLite）里占用唯一连接，
	// 结果集未关就发 COUNT 会同池自锁（无限连接池掩盖了这一顺序缺陷）。
	if err := t.db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT COUNT(*) FROM %s %s", tbl, w), args...).Scan(&page.Total); err != nil {
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
