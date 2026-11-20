// Package fleetstore persists the four-layer fleet declaration (defaults /
// sinks / formats / format_groups / machines / schedules) in SQLite so the
// console can edit it. The TOML file stays the seed document: while the
// store is empty the file is authoritative; the first console edit stores
// the whole document and from then on the store's tables win. Equivalence
// is structural — import(export(store)) reproduces the same document, and
// expanding the store-backed document yields the same component rows as the
// original TOML (see the round-trip tests).
package fleetstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dynamic-runtime/extensions/configwatch"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite"
)

// FleetDoc is one persisted fleet declaration. Values stay in the parsed
// TOML shapes (maps/slices/scalars) so the composition layer can consume
// them directly and the TOML export is a faithful re-rendering.
type FleetDoc struct {
	Defaults     map[string]any `json:"defaults,omitempty"`
	Sinks        []any          `json:"sinks,omitempty"`
	Formats      []any          `json:"formats,omitempty"`
	FormatGroups []any          `json:"format_groups,omitempty"`
	Machines     []any          `json:"machines,omitempty"`
	Schedules    []any          `json:"schedules,omitempty"`
}

// Keys lists the top-level tables this store owns (the four-layer model).
func (d FleetDoc) keys() map[string]any {
	out := map[string]any{}
	if d.Defaults != nil {
		out["defaults"] = d.Defaults
	}
	if d.Sinks != nil {
		out["sinks"] = d.Sinks
	}
	if d.Formats != nil {
		out["formats"] = d.Formats
	}
	if d.FormatGroups != nil {
		out["format_groups"] = d.FormatGroups
	}
	if d.Machines != nil {
		out["machines"] = d.Machines
	}
	if d.Schedules != nil {
		out["schedules"] = d.Schedules
	}
	return out
}

// AsBase renders the doc as a composition Base overlay: exactly the keys it
// owns, to override the seed file's declaration tables.
func (d FleetDoc) AsBase() map[string]any { return d.keys() }

// Empty reports whether the doc carries no declaration tables at all.
func (d FleetDoc) Empty() bool { return len(d.keys()) == 0 }

// Store is the SQLite-backed fleet document store. One row, one document —
// the console edits the whole declaration, never a partial merge.
type Store struct {
	db   *sql.DB
	path string
}

// Open creates (or opens) the store at path. The parent directory is
// created so a fresh deployment works on first edit.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("fleetstore: path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("fleetstore: %w", err)
		}
	}
	// busy_timeout：dump-config/-once 与运行中的 web-ui 并发读存储时
	// 等待写锁而不是立即报 SQLITE_BUSY。
	db, err := sql.Open("sqlite", dsnWithBusyTimeout(path))
	if err != nil {
		return nil, fmt.Errorf("fleetstore: %w", err)
	}
	// 单写者：配置编辑是低频操作，单连接串行即可，也杜绝多协程写竞争。
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS fleet_doc (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			doc TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		// 配置底座（Iteration 配置-G）：版本计数与审计历史。计数放 meta
		// 而不是 fleet_doc 行上——Reset 会删行，版本必须跨重置单调。
		`CREATE TABLE IF NOT EXISTS fleet_meta (
			k TEXT PRIMARY KEY,
			v TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS fleet_history (
			revision INTEGER PRIMARY KEY,
			doc TEXT NOT NULL,
			source TEXT NOT NULL,
			saved_at TEXT NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("fleetstore: %w", err)
		}
	}
	return &Store{db: db, path: path}, nil
}

// ErrRevisionConflict 是乐观并发控制的拒绝信号：提交携带的预期版本与当前
// 版本不符——另一个页面（或另一个入口）已经改过配置。
var ErrRevisionConflict = errors.New("fleetstore: 配置已被其他页面修改，请刷新后重试")

// ApplyStatus 是一次配置版本被运行时应用的结果快照。
type ApplyStatus struct {
	Revision int64     `json:"revision"`
	Status   string    `json:"status"` // applied | failed
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// HistoryEntry 是一条审计记录：谁（source）在何时把配置写成了什么（全量快照）。
type HistoryEntry struct {
	Revision int64     `json:"revision"`
	Source   string    `json:"source"`
	SavedAt  time.Time `json:"savedAt"`
	Doc      *FleetDoc `json:"doc"`
}

const (
	SourceConsole = "console"
	SourceImport  = "import"
	SourceReset   = "reset"
)

// metaGet/metaSet：版本计数与 seed 指纹都住在 fleet_meta。
func (s *Store) metaGet(key string) (string, bool, error) {
	var v string
	switch err := s.db.QueryRow(`SELECT v FROM fleet_meta WHERE k = ?`, key).Scan(&v); err {
	case nil:
		return v, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("fleetstore meta: %w", err)
	}
}

func (s *Store) metaSet(key, v string) error {
	if _, err := s.db.Exec(`INSERT INTO fleet_meta (k, v) VALUES (?, ?)
		ON CONFLICT (k) DO UPDATE SET v = excluded.v`, key, v); err != nil {
		return fmt.Errorf("fleetstore meta: %w", err)
	}
	return nil
}

// revisionIn/metaSetIn 是事务内版本：CAS 与三次写同一事务。
func (s *Store) revisionIn(tx *sql.Tx) (int64, error) {
	var v string
	switch err := tx.QueryRow(`SELECT v FROM fleet_meta WHERE k = 'revision'`).Scan(&v); err {
	case nil:
	case sql.ErrNoRows:
		return 0, nil
	default:
		return 0, fmt.Errorf("fleetstore meta: %w", err)
	}
	var n int64
	if _, err := fmt.Sscan(v, &n); err != nil {
		return 0, fmt.Errorf("fleetstore meta: revision %q: %w", v, err)
	}
	return n, nil
}

func (s *Store) metaSetIn(tx *sql.Tx, key, v string) error {
	if _, err := tx.Exec(`INSERT INTO fleet_meta (k, v) VALUES (?, ?)
		ON CONFLICT (k) DO UPDATE SET v = excluded.v`, key, v); err != nil {
		return fmt.Errorf("fleetstore meta: %w", err)
	}
	return nil
}

// Revision 返回当前版本号（没有文档时为 0）。版本跨重置单调递增。
func (s *Store) Revision() (int64, error) {
	v, ok, err := s.metaGet("revision")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	var n int64
	if _, err := fmt.Sscan(v, &n); err != nil {
		return 0, fmt.Errorf("fleetstore meta: revision %q: %w", v, err)
	}
	return n, nil
}

// SetSeedFingerprint 记录账本最近一次对应的 seed 文件指纹。文件重载时
// 指纹不一致 = 手改的文件四层表正被账本遮蔽——运行时据此把死信变响。
func (s *Store) SetSeedFingerprint(fp string) error {
	return s.metaSet("seed_fp", fp)
}

// SeedFingerprint 返回记录的 seed 指纹；ok=false 表示尚未记录。
func (s *Store) SeedFingerprint() (string, bool, error) {
	v, ok, err := s.metaGet("seed_fp")
	return v, ok, err
}

// Save 以乐观并发控制替换文档：expectRevision 必须等于当前版本（空库为 0）。
// 成功返回新版本；版本不符返回 ErrRevisionConflict，账本原样不动。每次成功
// 落一条历史（全量快照 + 来源）。
func (s *Store) Save(doc FleetDoc, expectRevision int64, source string) (int64, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return 0, fmt.Errorf("fleetstore save: %w", err)
	}
	// CAS + 三次写必须在同一事务里：否则（a）并发调用方可能双双通过 CAS、
	// 败者的 doc/revision 已落库而历史插入撞主键报错——调用方被告知失败
	// 但文档其实已写入；（b）中途失败（磁盘满）留下"版本推进了、审计没有"。
	// MaxOpenConns(1) 串行化连接，BEGIN IMMEDIATE 再挡住跨进程写者。
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("fleetstore save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := s.revisionIn(tx)
	if err != nil {
		return 0, err
	}
	if expectRevision != cur {
		return 0, fmt.Errorf("%w（当前版本 %d，提交携带 %d）", ErrRevisionConflict, cur, expectRevision)
	}
	next := cur + 1
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO fleet_doc (id, doc, updated_at) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET doc = excluded.doc, updated_at = excluded.updated_at`,
		string(raw), now); err != nil {
		return 0, fmt.Errorf("fleetstore save: %w", err)
	}
	if err := s.metaSetIn(tx, "revision", fmt.Sprint(next)); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO fleet_history (revision, doc, source, saved_at)
		VALUES (?, ?, ?, ?)`, next, string(raw), source, now); err != nil {
		return 0, fmt.Errorf("fleetstore history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("fleetstore save: %w", err)
	}
	return next, nil
}

// History 返回最近的审计记录（最新在前），limit <= 0 视为 10。
func (s *Store) History(limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`SELECT revision, doc, source, saved_at
		FROM fleet_history ORDER BY revision DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("fleetstore history: %w", err)
	}
	defer rows.Close()
	out := make([]HistoryEntry, 0, limit)
	for rows.Next() {
		var e HistoryEntry
		var raw, savedAt string
		if err := rows.Scan(&e.Revision, &raw, &e.Source, &savedAt); err != nil {
			return nil, fmt.Errorf("fleetstore history: %w", err)
		}
		at, err := time.Parse(time.RFC3339, savedAt)
		if err != nil {
			return nil, fmt.Errorf("fleetstore history: %w", err)
		}
		e.SavedAt = at
		var doc FleetDoc
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, fmt.Errorf("fleetstore history: %w", err)
		}
		e.Doc = &doc
		out = append(out, e)
	}
	return out, rows.Err()
}

func dsnWithBusyTimeout(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_pragma=busy_timeout(5000)"
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Path 返回存储的数据库路径（快照等伴生产物按它的目录落位）。
func (s *Store) Path() string { return s.path }

// Load returns the stored document; exists is false when nothing was ever
// saved (the TOML file stays authoritative).
func (s *Store) Load() (doc FleetDoc, exists bool, err error) {
	var raw string
	switch err := s.db.QueryRow(`SELECT doc FROM fleet_doc WHERE id = 1`).Scan(&raw); err {
	case nil:
	case sql.ErrNoRows:
		return FleetDoc{}, false, nil
	default:
		return FleetDoc{}, false, fmt.Errorf("fleetstore load: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return FleetDoc{}, true, fmt.Errorf("fleetstore load: %w", err)
	}
	doc.Normalize()
	return doc, true, nil
}

// Normalize 把 JSON 解码产生的 float64 还原为整型（整数值时）：TOML
// 解析整数得到 int64，组合层按 int64 消费（如 catchup_days）——存储
// 往返与编辑写回都必须还原出与文件解析相同的值形状，等价性才成立。
// 一切来自 JSON 的文档（编辑命令体、存储加载）在进入组合层前必须调用。
func (d *FleetDoc) Normalize() {
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case float64:
			if x == float64(int64(x)) {
				return int64(x)
			}
			return x
		case map[string]any:
			for k, item := range x {
				x[k] = walk(item)
			}
			return x
		case []any:
			for i, item := range x {
				x[i] = walk(item)
			}
			return x
		default:
			return v
		}
	}
	for k, v := range d.Defaults {
		d.Defaults[k] = walk(v)
	}
	for _, rows := range [][]any{d.Sinks, d.Formats, d.FormatGroups, d.Machines, d.Schedules} {
		for _, row := range rows {
			if m, ok := row.(map[string]any); ok {
				walk(m)
			}
		}
	}
}

// Reset drops the stored document: the TOML file becomes authoritative again.
// 版本照常推进并记录 tombstone——"回到文件权威"也是一次必须可审计的配置变更。
func (s *Store) Reset(expectRevision int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("fleetstore reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := s.revisionIn(tx)
	if err != nil {
		return 0, err
	}
	if expectRevision != cur {
		return 0, fmt.Errorf("%w（当前版本 %d，提交携带 %d）", ErrRevisionConflict, cur, expectRevision)
	}
	next := cur + 1
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`DELETE FROM fleet_doc WHERE id = 1`); err != nil {
		return 0, fmt.Errorf("fleetstore reset: %w", err)
	}
	if err := s.metaSetIn(tx, "revision", fmt.Sprint(next)); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO fleet_history (revision, doc, source, saved_at)
		VALUES (?, ?, ?, ?)`, next, "{}", SourceReset, now); err != nil {
		return 0, fmt.Errorf("fleetstore history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("fleetstore reset: %w", err)
	}
	return next, nil
}

// FleetDBPath 解析配置的 fleet 存储路径：defaults.state_dir（缺省
// ./state）下的 config.db。路径相对配置目录锚定后解析，与运行时的
// state_dir 行为一致（调用方需已 AnchorConfigDir）。
func FleetDBPath(configPath string) string {
	stateDir := "state"
	if raw, err := os.ReadFile(configPath); err == nil {
		if doc, err := configwatch.ParseDocument(raw); err == nil {
			if defaults, ok := doc["defaults"].(map[string]any); ok {
				if sd, ok := defaults["state_dir"].(string); ok && sd != "" {
					stateDir = sd
				}
			}
		}
	}
	return filepath.Join(stateDir, "config.db")
}

// LoadBase 只读地解析存储为组合叠加层：存储不存在或为空 → nil（文件
// 权威）。供 -once 采集、dump-config 等不经 WatchHost 的路径使用——
// 控制台编辑后的声明必须对每一个调和入口生效，否则就是静默分叉。
func LoadBase(dbPath string) (map[string]any, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	store, err := Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	doc, exists, err := store.Load()
	if err != nil || !exists {
		return nil, err
	}
	if doc.Empty() {
		return nil, nil
	}
	return doc.AsBase(), nil
}
