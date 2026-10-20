package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// 雪花发号：位布局、单调性、回拨与序号耗尽的处理、基线、跨实例登记。

func fakeFlakeClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	prev := flakeNow
	flakeNow = func() time.Time { return cur }
	t.Cleanup(func() { flakeNow = prev })
	return &cur
}

func TestFlakeMonotonicUnique(t *testing.T) {
	g := newFileKeyGen(1234, 0)
	const n = 100000
	var prev int64
	seen := make(map[int64]struct{}, n)
	for i := 0; i < n; i++ {
		k := g.next()
		if k <= prev {
			t.Fatalf("key not increasing at %d: %d after %d", i, k, prev)
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("duplicate key %d at %d", k, i)
		}
		seen[k] = struct{}{}
		prev = k
	}
	if got := flakeNodeOf(prev); got != 1234 {
		t.Fatalf("node bits = %d, want 1234", got)
	}
	if ts := flakeTimeOf(prev); time.Since(ts) > time.Minute || time.Until(ts) > time.Minute {
		t.Fatalf("timestamp field drifted: %s", ts)
	}
}

func TestFlakeClockRollbackFreezes(t *testing.T) {
	base := time.Now()
	clock := fakeFlakeClock(t, base)
	g := newFileKeyGen(7, 0)
	first := g.next()
	*clock = base.Add(-5 * time.Second) // NTP 回拨 5 秒
	second := g.next()
	if second <= first {
		t.Fatalf("rollback produced non-increasing key: %d after %d", second, first)
	}
	// 冻结语义：时间戳字段不跟着回拨。
	if a, b := flakeTimeOf(first), flakeTimeOf(second); b.Before(a) {
		t.Fatalf("timestamp went backwards: %s -> %s", a, b)
	}
	*clock = base.Add(2 * time.Second) // 时钟追回来
	third := g.next()
	if third <= second {
		t.Fatalf("catch-up produced non-increasing key: %d after %d", third, second)
	}
}

func TestFlakeSeqExhaustionBorrowsNextMs(t *testing.T) {
	clock := fakeFlakeClock(t, time.Now())
	g := newFileKeyGen(1, 0)
	var last int64
	// 首个键吃序号 1（lastMs 已在构造时等于 now），第 63 个键吃到 63。
	for i := 0; i < flakeSeqMax; i++ {
		last = g.next()
	}
	if got := flakeSeqOf(last); got != flakeSeqMax {
		t.Fatalf("expected seq to reach %d, got %d", flakeSeqMax, got)
	}
	first := last
	*clock = time.UnixMilli(flakeEpoch + g.lastMs) // 时钟不动
	next := g.next()
	if next <= first {
		t.Fatalf("seq overflow must still increase: %d after %d", next, first)
	}
	if flakeSeqOf(next) != 0 {
		t.Fatalf("seq after overflow = %d, want 0", flakeSeqOf(next))
	}
	if got := next >> flakeTimeShift; got != first>>flakeTimeShift+1 {
		t.Fatalf("overflow must borrow exactly 1ms: %d -> %d", first>>flakeTimeShift, got)
	}
}

func TestFlakeBaselineAboveExistingKey(t *testing.T) {
	nowMs := time.Now().UnixMilli() - flakeEpoch
	future := ((nowMs + 3600_000) << flakeTimeShift) | (7 << flakeNodeShift) | 5
	g := newFileKeyGen(3, future)
	if k := g.next(); k <= future {
		t.Fatalf("generator must start above the registry MAX (%d), got %d", future, k)
	}
	// 旧计数器的小键（1..N）解不出有意义的时间戳，退回到 now 即可。
	legacy := newFileKeyGen(3, 42)
	k := legacy.next()
	if ts := flakeTimeOf(k); time.Since(ts) > time.Minute || time.Until(ts) > time.Minute {
		t.Fatalf("legacy baseline must fall back to now, got %s", ts)
	}
}

func TestFileKeyNodeValidation(t *testing.T) {
	bad := 70000
	cfg := TableConfig{Driver: "sqlite", DSN: "x", Dialect: "sqlite", Table: "t", FileTable: "reg", FileKey: true, FileKeyNode: &bad,
		Columns: []ColumnMapping{{Name: "a", Column: "a", Type: "text"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("out-of-range file_key_node must be rejected")
	}
	if _, err := OpenTable(context.Background(), cfg); err == nil {
		t.Fatal("OpenTable must reject out-of-range file_key_node")
	}
}

// openFileKeyStore 打开一个 file_key 骨架的 sqlite 库，返回存储与库文件路径。
func openFileKeyStore(t *testing.T, dsn string, node *int) *TableStorage {
	t.Helper()
	cfg := TableConfig{
		Driver: "sqlite", DSN: dsn, Dialect: "sqlite",
		Table: "hc", FileTable: "reg", FileKey: true, FileKeyNode: node,
		Columns: hcColumns(),
	}
	tw, err := OpenTable(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tw.Close() })
	return tw
}

func fkBatch(name, hash string) model.Batch {
	b := hcBatch([]string{"a", "b"}, []string{"1", "2"})
	b.File = model.FileIdentity{SourceID: "m", Path: "/" + name, Name: name, Hash: hash}
	return b
}

// 计数器表已退役：schema 阶段不再建 <file_table>_file_key_seq。
func TestFileKeyCounterTableRetired(t *testing.T) {
	tw := openFileKeyStore(t, filepath.Join(t.TempDir(), "k.db"), nil)
	var n int
	if err := tw.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'reg_file_key_seq'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("counter table must not be created anymore")
	}
	if tw.fileKeyGen.Load() == nil {
		t.Fatal("generator must be initialized at schema time")
	}
}

// 两个实例（不同节点号）登记不同文件：键互不相同、都落库、不带计数器的串行点。
func TestFileKeyTwoInstancesDistinctKeys(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "k.db")
	n0, n1 := 0, 1
	a := openFileKeyStore(t, dsn, &n0)
	b := openFileKeyStore(t, dsn, &n1)
	ctx := context.Background()
	if err := a.Write(ctx, fkBatch("a.log", "ha")); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(ctx, fkBatch("b.log", "hb")); err != nil {
		t.Fatal(err)
	}
	rows, err := a.db.Query("SELECT name, file_key FROM reg ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keys := map[string]int64{}
	for rows.Next() {
		var name string
		var key int64
		if err := rows.Scan(&name, &key); err != nil {
			t.Fatal(err)
		}
		keys[name] = key
	}
	if keys["a.log"] == keys["b.log"] || keys["a.log"] == 0 || keys["b.log"] == 0 {
		t.Fatalf("distinct files must get distinct non-zero keys: %#v", keys)
	}
	if flakeNodeOf(keys["a.log"]) != 0 || flakeNodeOf(keys["b.log"]) != 1 {
		t.Fatalf("pinned node ids not honoured: %#v", keys)
	}
	// 数据行按各自的键落库。
	var landed int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM hc WHERE file_key IN (?, ?)", keys["a.log"], keys["b.log"]).Scan(&landed); err != nil {
		t.Fatal(err)
	}
	if landed != 2 {
		t.Fatalf("data rows = %d, want 2", landed)
	}
}

// 同一文件重复写（重试/重采）：沿用注册表里的键，不再发新号。
func TestFileKeyReusesRegistryKey(t *testing.T) {
	tw := openFileKeyStore(t, filepath.Join(t.TempDir(), "k.db"), nil)
	ctx := context.Background()
	b := fkBatch("a.log", "ha")
	if err := tw.Write(ctx, b); err != nil {
		t.Fatal(err)
	}
	var first int64
	if err := tw.db.QueryRow("SELECT file_key FROM reg WHERE file_id = ?", b.File.Identity()).Scan(&first); err != nil {
		t.Fatal(err)
	}
	// 换一个行号再写一遍，避免 upsert 命中同一行掩盖问题。
	b.Records[0].RowNumber = 2
	if err := tw.Write(ctx, b); err != nil {
		t.Fatal(err)
	}
	var again int64
	if err := tw.db.QueryRow("SELECT file_key FROM reg WHERE file_id = ?", b.File.Identity()).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("registry key changed on rewrite: %d -> %d", first, again)
	}
}

// 存量注册行（file_key 为 NULL）：就地补一个雪花键，数据行挂到这个键上。
func TestFileKeyLegacyBackfill(t *testing.T) {
	tw := openFileKeyStore(t, filepath.Join(t.TempDir(), "k.db"), nil)
	b := fkBatch("a.log", "ha")
	if _, err := tw.db.Exec("INSERT INTO reg (source_id, collection_date, file_id, path, name, records, header, collected_at) VALUES (?, ?, ?, ?, ?, 0, 'a,b', NULL)",
		"m", "2026-09-17", b.File.Identity(), b.File.Path, b.File.Name); err != nil {
		t.Fatal(err)
	}
	if err := tw.Write(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	var key int64
	if err := tw.db.QueryRow("SELECT file_key FROM reg WHERE file_id = ?", b.File.Identity()).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key <= 0 {
		t.Fatalf("legacy row must be backfilled with a snowflake key, got %d", key)
	}
	var landed int
	if err := tw.db.QueryRow("SELECT COUNT(*) FROM hc WHERE file_key = ?", key).Scan(&landed); err != nil {
		t.Fatal(err)
	}
	if landed != 1 {
		t.Fatalf("data rows under backfilled key = %d, want 1", landed)
	}
}

// 同一节点号、同一毫秒、同一序号的两个发号器必然撞 (file_key) 唯一索引：
// 后到的整批按约束错误失败（没有可回查的注册行）；重试时发号器前进一个
// 序号、换新键成功。这是唯一会在重试时换键的失败路径。
func TestFileKeyCollisionFailsThenRetriesWithNewKey(t *testing.T) {
	fakeFlakeClock(t, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	dsn := filepath.Join(t.TempDir(), "k.db")
	node := 7
	a := openFileKeyStore(t, dsn, &node)
	b := openFileKeyStore(t, dsn, &node) // 都在任何写入之前初始化 → 基线同为 0
	ctx := context.Background()
	if err := a.Write(ctx, fkBatch("a.log", "ha")); err != nil {
		t.Fatal(err)
	}
	err := b.Write(ctx, fkBatch("b.log", "hb"))
	if err == nil {
		t.Fatal("generators sharing node/ms/seq must collide on file_key")
	}
	if !errs.Is(err, errs.ErrStorageConstraint) {
		t.Fatalf("collision must fail as a constraint error, got %v", err)
	}
	if err := b.Write(ctx, fkBatch("b.log", "hb")); err != nil {
		t.Fatalf("retry after collision must allocate a new key and succeed: %v", err)
	}
	keys := map[string]int64{}
	rows, err := a.db.Query("SELECT name, file_key FROM reg")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var key int64
		if err := rows.Scan(&name, &key); err != nil {
			t.Fatal(err)
		}
		keys[name] = key
	}
	if keys["a.log"] == 0 || keys["b.log"] == 0 || keys["a.log"] == keys["b.log"] {
		t.Fatalf("retry must produce distinct non-zero keys: %#v", keys)
	}
	for _, name := range []string{"a.log", "b.log"} {
		var landed int
		if err := a.db.QueryRow("SELECT COUNT(*) FROM hc WHERE file_key = ?", keys[name]).Scan(&landed); err != nil {
			t.Fatal(err)
		}
		if landed != 1 {
			t.Fatalf("data rows under %s = %d, want 1", name, landed)
		}
	}
}

// 三元组 (source_id, collection_date, file_id) 冲突：同一文件已被先到者登记，
// 后到者的 insert-if-missing 命中三元组而空转；随后回查应拿到**胜出者的键**，
// 而不是本次多发的号。数据行按 (file_key, row_number) 幂等，不重复。
func TestFileKeyTripleConflictKeepsWinnerKey(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "k.db")
	n0, n1 := 0, 1
	a := openFileKeyStore(t, dsn, &n0)
	b := openFileKeyStore(t, dsn, &n1)
	ctx := context.Background()
	batch := fkBatch("a.log", "ha")
	if err := a.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var winner int64
	if err := a.db.QueryRow("SELECT file_key FROM reg WHERE file_id = ?", batch.File.Identity()).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	// b 自己发一个键，再模拟"登记时才发现三元组已被先到者占住"。
	keyB, err := b.nextFileKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if keyB == winner {
		t.Fatalf("test setup: b must not draw the winner's key, both %d", keyB)
	}
	cols := []string{"file_key", "source_id", "collection_date", "file_id", "path", "name", "records", "header", "collected_at"}
	stmt := insertIfMissingStmt("sqlite", "reg", cols, "source_id, collection_date, file_id")
	res, err := b.db.ExecContext(ctx, stmt, keyB, string(batch.Key.SourceID), b.dateBind(batch.Key.Date),
		batch.File.Identity(), batch.File.Path, batch.File.Name, int64(0), strings.Join(batch.Header, ","), time.Now())
	if err != nil {
		t.Fatalf("insert on triple conflict must be a no-op, got %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 0 {
		t.Fatalf("insert on triple conflict affected %d rows (err %v), want 0", n, err)
	}
	var after int64
	if err := b.db.QueryRow("SELECT file_key FROM reg WHERE file_id = ?", batch.File.Identity()).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != winner {
		t.Fatalf("triple conflict changed the winner key: %d -> %d", winner, after)
	}
	got, err := b.resolveFileKey(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if got != winner {
		t.Fatalf("resolve after triple conflict = %d, want winner %d", got, winner)
	}
	var orphan int
	if err := b.db.QueryRow("SELECT COUNT(*) FROM hc WHERE file_key = ?", keyB).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if orphan != 0 {
		t.Fatalf("discarded key must not carry data, got %d rows", orphan)
	}
	if err := b.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := b.db.QueryRow("SELECT COUNT(*) FROM hc WHERE file_key = ?", winner).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("retry from another generator must stay idempotent: %d rows under winner, want 1", total)
	}
}
