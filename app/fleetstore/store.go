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
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	db *sql.DB
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
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("fleetstore: %w", err)
	}
	// 单写者：配置编辑是低频操作，单连接串行即可，也杜绝多协程写竞争。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS fleet_doc (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		doc TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("fleetstore: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

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
	doc.normalize()
	return doc, true, nil
}

// normalize 把 JSON 解码产生的 float64 还原为整型（整数值时）：TOML
// 解析整数得到 int64，组合层按 int64 消费（如 catchup_days）——存储
// 往返必须还原出与文件解析相同的值形状，等价性才成立。
func (d *FleetDoc) normalize() {
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

// Save replaces the stored document (idempotent upsert of the single row).
func (s *Store) Save(doc FleetDoc) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("fleetstore save: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO fleet_doc (id, doc, updated_at) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET doc = excluded.doc, updated_at = excluded.updated_at`,
		string(raw), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("fleetstore save: %w", err)
	}
	return nil
}

// Reset drops the stored document: the TOML file becomes authoritative again.
func (s *Store) Reset() error {
	if _, err := s.db.Exec(`DELETE FROM fleet_doc WHERE id = 1`); err != nil {
		return fmt.Errorf("fleetstore reset: %w", err)
	}
	return nil
}
