package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

var tableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SQLConfig describes one database-backed Storage. Driver is the Go
// database/sql driver name registered by the process importing it.
type SQLConfig struct {
	Driver  string
	DSN     string
	Dialect string
	Table   string
}

func (c SQLConfig) Validate() error {
	if c.Driver == "" || c.DSN == "" {
		return errs.Sourcef(errs.ErrInvalidConfig, "storage dsn/driver required")
	}
	if c.Dialect == "" {
		c.Dialect = "mysql"
	}
	c.Dialect = strings.ToLower(c.Dialect)
	if c.Table == "" {
		c.Table = "gocordis_records"
	}
	if !tableNamePattern.MatchString(c.Table) {
		return errs.Sourcef(errs.ErrInvalidConfig, "storage table %q is not a simple identifier", c.Table)
	}
	return nil
}

// SQLStore stores generic CSV records in a target database. The row key is
// source_id + collection_date + file_id + row_number, which makes repeated
// writes idempotent.
//
// Two construction modes exist. OpenSQL connects eagerly: an unreachable
// target fails the component activation. OpenLazySQL defers every connection
// to the first Write and re-connects after connection loss, so a deployment
// whose remote database is temporarily down still starts; files fail, stay in
// the local failure ledger, and the next trigger replays them.
type SQLStore struct {
	db      *sql.DB
	lazy    bool
	cfg     SQLConfig
	dialect string
	table   string
	closeMe func() error
}

func OpenSQL(ctx context.Context, cfg SQLConfig) (*SQLStore, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	db, release, err := acquireDB(cfg.Dialect, cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		release()
		return nil, errs.ClassifyStorageError("ping", err)
	}
	s := &SQLStore{db: db, cfg: cfg, dialect: strings.ToLower(cfg.Dialect), table: cfg.Table, closeMe: releaseClose(release)}
	if err := s.ensureSchema(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// OpenLazySQL creates a lazily connecting store. No network I/O happens here,
// so an unreachable remote database cannot prevent component activation.
func OpenLazySQL(cfg SQLConfig) (*SQLStore, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &SQLStore{lazy: true, cfg: cfg, dialect: strings.ToLower(cfg.Dialect), table: cfg.Table}, nil
}

// connect opens one eager connection attempt for a lazy store: open, ping,
// and schema ensure. The schema statements are idempotent, so reconnecting
// after an outage re-checks it safely.
func (s *SQLStore) connect(ctx context.Context) error {
	db, release, err := acquireDB(s.cfg.Dialect, s.cfg.Driver, s.cfg.DSN)
	if err != nil {
		return errs.ClassifyStorageError("open", err)
	}
	if err := db.PingContext(ctx); err != nil {
		release()
		return errs.ClassifyStorageError("ping", err)
	}
	s.db = db
	s.closeMe = releaseClose(release)
	if err := s.ensureSchema(ctx); err != nil {
		_ = s.Close()
		s.db = nil
		s.closeMe = nil
		return err
	}
	return nil
}

// reset discards a broken pooled connection so the next Write reconnects.
func (s *SQLStore) reset() {
	if s.closeMe != nil {
		_ = s.closeMe()
	}
	s.db = nil
	s.closeMe = nil
}

func (s *SQLStore) Write(ctx context.Context, batch model.Batch) error {
	if len(batch.Records) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.db == nil {
		if !s.lazy {
			return errs.ClassifyStorageError("closed", sql.ErrConnDone)
		}
		if err := s.connect(ctx); err != nil {
			return err
		}
	}
	// A ping keeps the outage story simple: when the remote database went
	// away since the last write, the file fails here with a classified
	// retriable error and stays in the local failure ledger.
	if err := s.db.PingContext(ctx); err != nil {
		s.reset()
		if !s.lazy {
			return errs.ClassifyStorageError("ping", err)
		}
		if err := s.connect(ctx); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.reset()
		return errs.ClassifyStorageError("begin", err)
	}
	defer tx.Rollback()
	for i := range batch.Records {
		rec := &batch.Records[i]
		payload, err := json.Marshal(rec.Fields)
		if err != nil {
			return errs.Sourcef(errs.ErrStoragePermanent, "encode row %d: %w", rec.RowNumber, err)
		}
		args := []any{
			string(batch.Key.SourceID),
			batch.Key.Date.String(),
			batch.File.Identity(),
			rec.RowNumber,
			string(payload),
			string(payload),
			time.Now(),
		}
		sqlText, err := s.upsertSQL(len(args))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, sqlText, args...); err != nil {
			return errs.ClassifyStorageError("write row "+batch.File.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return errs.ClassifyStorageError("commit", err)
	}
	return nil
}

func (s *SQLStore) ensureSchema(ctx context.Context) error {
	t := quoteIdent(s.dialect, s.table)
	var stmt string
	switch s.dialect {
	case "mysql":
		stmt = fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (source_id VARCHAR(255) NOT NULL, collection_date VARCHAR(32) NOT NULL, file_id VARCHAR(1024) NOT NULL, row_number BIGINT NOT NULL, row_values TEXT NOT NULL, payload TEXT NOT NULL, created_at TIMESTAMP(6), PRIMARY KEY (source_id, collection_date, file_id, row_number))", t)
	case "postgres":
		stmt = fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (source_id TEXT NOT NULL, collection_date TEXT NOT NULL, file_id TEXT NOT NULL, row_number BIGINT NOT NULL, row_values JSONB NOT NULL, payload TEXT NOT NULL, created_at TIMESTAMPTZ, PRIMARY KEY (source_id, collection_date, file_id, row_number))", t)
	case "oracle":
		stmt = fmt.Sprintf("BEGIN EXECUTE IMMEDIATE 'CREATE TABLE %s (source_id VARCHAR2(255) NOT NULL, collection_date VARCHAR2(32) NOT NULL, file_id VARCHAR2(1024) NOT NULL, row_number NUMBER(38) NOT NULL, row_values CLOB NOT NULL, payload CLOB NOT NULL, created_at TIMESTAMP, CONSTRAINT pk_%s PRIMARY KEY (source_id, collection_date, file_id, row_number))'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -955 THEN RAISE; END IF; END", t, safeConstraintName(s.table))
	case "sqlite":
		stmt = fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (source_id TEXT NOT NULL, collection_date TEXT NOT NULL, file_id TEXT NOT NULL, row_number INTEGER NOT NULL, row_values TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT, PRIMARY KEY (source_id, collection_date, file_id, row_number))", t)
	case "sqlserver":
		stmt = fmt.Sprintf("IF OBJECT_ID(N'%s', N'U') IS NULL CREATE TABLE %s (source_id NVARCHAR(255) NOT NULL, collection_date NVARCHAR(32) NOT NULL, file_id NVARCHAR(1024) NOT NULL, row_number BIGINT NOT NULL, row_values NVARCHAR(MAX) NOT NULL, payload NVARCHAR(MAX) NOT NULL, created_at DATETIME2, PRIMARY KEY (source_id, collection_date, file_id, row_number))", s.table, t)
	default:
		return fmt.Errorf("%w: unsupported sql dialect %q", errs.ErrInvalidConfig, s.dialect)
	}
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		return errs.ClassifyStorageError("ensure schema", err)
	}
	return nil
}

func (s *SQLStore) upsertSQL(argCount int) (string, error) {
	t := quoteIdent(s.dialect, s.table)
	cols := []string{"source_id", "collection_date", "file_id", "row_number", "row_values", "payload", "created_at"}
	placeholders := placeholders(s.dialect, 1, len(cols))
	joined := strings.Join(cols, ", ")
	switch s.dialect {
	case "mysql":
		return fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES (%s)", t, joined, placeholders), nil
	case "postgres", "sqlite":
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (source_id, collection_date, file_id, row_number) DO NOTHING", t, joined, placeholders), nil
	case "oracle", "sqlserver":
		dual := ""
		if s.dialect == "oracle" {
			dual = " FROM DUAL"
		}
		columns := []string{"source_id", "collection_date", "file_id", "row_number", "row_values", "payload", "created_at"}
		var selected, icols, ivals, on []string
		for i, col := range columns {
			selected = append(selected, fmt.Sprintf(":%d AS %s", i+1, col))
			icols = append(icols, col)
			ivals = append(ivals, "src."+col)
			if i < 4 { // 前四列 = 主键
				on = append(on, fmt.Sprintf("dst.%s = src.%s", col, col))
			}
		}
		return fmt.Sprintf("MERGE INTO %s dst USING (SELECT %s%s) src ON (%s) WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
			t, strings.Join(selected, ", "), dual, strings.Join(on, " AND "),
			strings.Join(icols, ", "), strings.Join(ivals, ", ")), nil
	default:
		return "", fmt.Errorf("%w: unsupported sql dialect %q", errs.ErrInvalidConfig, s.dialect)
	}
}

func (s *SQLStore) Close() error {
	if s.closeMe == nil {
		return nil
	}
	err := s.closeMe()
	s.db = nil
	s.closeMe = nil
	return err
}

func quoteIdent(dialect, name string) string {
	if tableNamePattern.MatchString(name) {
		switch dialect {
		case "mysql":
			return "`" + name + "`"
		case "oracle":
			// Oracle：一律加引号。裸标识符会撞保留字（SIZE/DATE/LEVEL/
			// COMMENT...，ORA-00904），且引号内大写与服务器折叠行为一致
			//（生成侧统一 ToUpper，两侧大小写匹配）。
			return `"` + strings.ToUpper(name) + `"`
		case "sqlserver":
			// SQL Server：[] 引用，保留字安全且与 sys.columns 输出匹配。
			return "[" + name + "]"
		}
		return `"` + name + `"`
	}
	// 非常规标识符（自动字段映射的列名来自 CSV 表头，是任意文本）：
	// 显式引号包裹并转义内嵌引号——这是注入与语法错误的唯一防线，
	// 引号内内容永不参与 SQL 解析。
	if dialect == "mysql" {
		return "`" + strings.ReplaceAll(strings.ReplaceAll(name, "`", "``"), "\x00", "") + "`"
	}
	if dialect == "sqlserver" {
		return "[" + strings.ReplaceAll(strings.ReplaceAll(name, "]", "]]"), "\x00", "") + "]"
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(name, `"`, `""`), "\x00", "") + `"`
}

func safeConstraintName(name string) string {
	name = strings.ReplaceAll(name, "_", "")
	if len(name) > 20 {
		name = name[:20]
	}
	if name == "" {
		return "records"
	}
	return name
}

func placeholders(dialect string, start, n int) string {
	var out []string
	for i := 0; i < n; i++ {
		switch dialect {
		case "postgres":
			out = append(out, fmt.Sprintf("$%d", start+i))
		case "oracle":
			out = append(out, fmt.Sprintf(":%d", start+i))
		case "sqlserver":
			out = append(out, fmt.Sprintf("@p%d", start+i))
		default:
			out = append(out, "?")
		}
	}
	return strings.Join(out, ", ")
}

var _ model.Storage = (*SQLStore)(nil)
