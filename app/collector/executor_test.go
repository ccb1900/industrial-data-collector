package collector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/errs"
	appmetadata "gocordis-csv-collector/app/metadata"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/parser"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/source"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
)

func date(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}

func newExecutor(t *testing.T, root string, st *state.MemoryState, mem *storage.MemoryStore) *Executor {
	t.Helper()
	src := source.New("prod", root, "*.csv", 0)
	parser := parser.New()
	parser.Header = true
	return &Executor{
		Source:   src,
		Parser:   parser,
		Storage:  mem,
		State:    st,
		Recovery: recovery.Planner{State: st},
		Config: Config{
			BatchSize:  2,
			DatePolicy: datepolicy.Policy{Type: datepolicy.PolicySpecific, Specific: date(t, "2026-09-06")},
		},
	}
}

func writeDateCSV(t *testing.T, root, day, name, body string) {
	t.Helper()
	dir := filepath.Join(root, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorStoresFilesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSucceeded || res[0].Records != 2 {
		t.Fatalf("result = %#v", res)
	}
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if mem.Total() != 2 {
		t.Fatalf("total rows = %d, want 2", mem.Total())
	}
}

func TestCollectorPartialFileFailureSkipsCompletedFiles(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	writeDateCSV(t, root, "2026-09-06", "b.csv", "id,name\n\"bad,2\n")
	writeDateCSV(t, root, "2026-09-06", "c.csv", "id,name\n3,c\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	_, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("malformed b.csv must fail collection")
	}
	if mem.Total() != 2 {
		t.Fatalf("a+c rows = %d, want 2", mem.Total())
	}
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("retry must still report malformed b.csv")
	}
	if mem.Total() != 2 {
		t.Fatalf("retry duplicated rows: total=%d", mem.Total())
	}
}

// blockingParser 让流一直读到 ctx 取消：模拟采集中途进程收到停止信号。
type blockingParser struct{ started chan struct{} }

func (p *blockingParser) Parse(ctx context.Context, _ io.Reader) (model.CSVDocument, error) {
	return model.CSVDocument{Data: &blockingStream{ctx: ctx, started: p.started}}, nil
}

type blockingStream struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
}

func (s *blockingStream) Header() []string { return []string{"id"} }

func (s *blockingStream) Next() (model.Record, error) {
	s.once.Do(func() { close(s.started) })
	<-s.ctx.Done()
	return model.Record{}, s.ctx.Err()
}

// 取消后终态必须落账：Running 行会被 Begin 的 24h 租约和 ListIncomplete
// 同时挡住，只有进程重启才会回收——服务停止再启动的常见操作会让日期
// 静默停采一天。
func TestCanceledCollectionEndsPendingNotRunning(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id\n1\n")
	ctx, cancel := context.WithCancel(context.Background())
	st := state.NewMemory()
	e := newExecutor(t, root, st, storage.NewMemory(storage.MemoryOptions{}))
	bp := &blockingParser{started: make(chan struct{})}
	e.Parser = bp

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.Handle(ctx, model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	}()
	select {
	case <-bp.started:
	case <-time.After(5 * time.Second):
		t.Fatal("collection never reached the parser")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collection did not return after cancel")
	}

	key := model.CollectionKey{SourceID: "prod", Date: date(t, "2026-09-06")}
	got, ok, err := st.StatusOf(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != model.StatusPending {
		t.Fatalf("state after cancel = %q (found=%v), want Pending", got, ok)
	}
	incomplete, err := st.ListIncomplete(context.Background(), "prod", key.Date, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 1 {
		t.Fatalf("canceled date must stay listed for recovery, got %v", incomplete)
	}
}

// 重试时已完成的文件走 FileCompleted 短路，必须回报台账里的真实行数：
// 带 0 发 FileCompleted 会把读模型里的记录数覆盖成 0。
func TestRetryReportsRecordsOfAlreadyCompletedFiles(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	writeDateCSV(t, root, "2026-09-06", "b.csv", "id,name\n\"bad\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	if _, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "pass1", Date: ptr(date(t, "2026-09-06"))}); err == nil {
		t.Fatal("malformed b.csv must fail the run")
	}
	if mem.Total() != 2 {
		t.Fatalf("rows after pass1 = %d, want 2 (a.csv only)", mem.Total())
	}

	// 修正 b.csv（size 变化 → 新身份），a.csv 保持不变走已完成短路。
	writeDateCSV(t, root, "2026-09-06", "b.csv", "id,name\n3,c\n4,d\n5,e\n")
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "pass2", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSucceeded {
		t.Fatalf("result = %#v, want Succeeded", res)
	}
	if res[0].Records != 5 {
		t.Fatalf("result records = %d, want 5 (2 stored for a.csv + 3 new)", res[0].Records)
	}
	if got := mem.Total(); got != 5 {
		t.Fatalf("stored rows = %d, want 5", got)
	}
}

func TestMissingDirectoryForPastDaySkips(t *testing.T) {
	root := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	// 实例制：过期日目录不存在 = 无证据，不物化台账。结果里的 Skipped
	// 只是日志语义；状态层不留下任何记录，补采列表也不会出现伪任务。
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSkipped {
		t.Fatalf("result = %#v, want Skipped (log-only)", res)
	}
	if _, ok, _ := st.StatusOf(context.Background(), model.CollectionKey{SourceID: "prod", Date: date(t, "2026-09-06")}); ok {
		t.Fatal("missing date must not be materialized into the ledger")
	}
	// 重跑同样不物化。
	res2, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 1 || res2[0].Status != model.StatusSkipped {
		t.Fatalf("re-run = %#v, want Skipped (log-only)", res2)
	}
}

// since 生命周期下界：早于它的业务日不计划、不物化；显式指定日期的
// 触发同样遵守。
func TestSinceClampsPlanning(t *testing.T) {
	root := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	since := date(t, "2026-09-05")
	e.Config.Since = since

	writeDateCSV(t, root, "2026-09-04", "old.csv", "id\n1\n")
	writeDateCSV(t, root, "2026-09-06", "new.csv", "id\n1\n")

	// 显式指定 since 之前的日期：返回带原因的空结果，不物化台账。
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-04"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSkipped || !strings.Contains(res[0].Error, "before source since") {
		t.Fatalf("before-since trigger = %#v, want clamped skip with reason", res)
	}
	if _, ok, _ := st.StatusOf(context.Background(), model.CollectionKey{SourceID: "prod", Date: date(t, "2026-09-04")}); ok {
		t.Fatal("before-since date must not be materialized")
	}
	// since 之后的日期正常采集。
	res2, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 1 || res2[0].Status != model.StatusSucceeded {
		t.Fatalf("after-since result = %#v, want Succeeded", res2)
	}
}

// TestMissingDirectoryTodayNotMaterialized 的前提变了。原来它验的是"探今天，
// 目录还没出现 → 不物化"；现在 dated 源根本不许探今天（date.Boundary 在 Handle
// 入口就拒），所以"不物化"由入口拒绝直接保证，强度更高。这里两侧都钉住：
// dated 源 → 边界拒绝且台账无行；same-day 源（仪表导出）→ 真的去探，目录未出现
// 时仍然不物化（文件晚到后下次触发重探即得，预期缺失由窗口内的重扫负责）。
func TestMissingDirectoryTodayNotMaterialized(t *testing.T) {
	root := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	today := model.NewCollectionDate(time.Now())

	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(today)})
	if !errs.Is(err, errs.ErrDateBoundary) {
		t.Fatalf("dated source must refuse today outright, got err=%v res=%#v", err, res)
	}
	if len(res) != 0 {
		t.Errorf("refused request must produce no results: %#v", res)
	}
	if incomplete, _ := st.ListIncomplete(context.Background(), "prod", today, 0); len(incomplete) != 0 {
		t.Fatalf("missing date must not be materialized, got %v", incomplete)
	}

	// same-day 源：仍然不物化，但走的是"探了、没有证据"这条路。
	e.Config.Boundary.SameDay = true
	res, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "watch", Date: ptr(today)})
	if err != nil {
		t.Fatalf("same-day source must probe today: %v", err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSkipped {
		t.Fatalf("result = %#v, want Skipped (log-only)", res)
	}
	if incomplete, _ := st.ListIncomplete(context.Background(), "prod", today, 0); len(incomplete) != 0 {
		t.Fatalf("same-day missing directory must not be materialized, got %v", incomplete)
	}
}

func ptr(d model.CollectionDate) *model.CollectionDate { return &d }

// unavailableSource simulates a UNC share that is temporarily unreachable —
// a TRANSIENT infrastructure condition that must be retried, never closed
// as terminal Skipped.
type unavailableSource struct{ id model.SourceID }

func (s unavailableSource) ID() model.SourceID { return s.id }
func (s unavailableSource) Root() string       { return `\\machine001\\data` }
func (s unavailableSource) List(context.Context, model.ListRequest) ([]model.FileIdentity, error) {
	return nil, errs.Sourcef(errs.ErrUnavailable, `path "\\machine001\\data": host unreachable`)
}
func (s unavailableSource) Read(context.Context, model.FileIdentity) (io.ReadCloser, error) {
	return nil, errs.Sourcef(errs.ErrUnavailable, "unreachable")
}
func (s unavailableSource) Close() error { return nil }

func TestUnreachableShareStaysFailedAndRetried(t *testing.T) {
	st := state.NewMemory()
	e := &Executor{
		Source:   unavailableSource{id: "prod"},
		Parser:   parser.New(),
		Storage:  storage.NewMemory(storage.MemoryOptions{}),
		State:    st,
		Recovery: recovery.Planner{State: st},
		Config: Config{
			BatchSize:  2,
			DatePolicy: datepolicy.Policy{Type: datepolicy.PolicySpecific, Specific: date(t, "2026-09-11")},
		},
	}
	// A fully past day, but the failure is the SHARE being unreachable — a
	// transient condition. It must close as Failed and stay listed for
	// retry, never terminal Skipped.
	// Handle reports the failure as its error AND as a Failed result row.
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-11"))})
	if err == nil || !strings.Contains(err.Error(), "host unreachable") {
		t.Fatalf("err = %v, want the unreachable failure", err)
	}
	if len(res) != 1 || res[0].Status != model.StatusFailed {
		t.Fatalf("result = %#v, want Failed", res)
	}
	incomplete, _ := st.ListIncomplete(context.Background(), "prod", date(t, "2026-09-11"), 0)
	if len(incomplete) != 1 {
		t.Fatalf("unreachable date must stay listed for recovery, got %v", incomplete)
	}
}

// 不稳定（稳定窗口内）≠ 空：执行器必须保持 Pending 重试，
// 绝不能折叠成"成功-0 文件"把日期终态化（晚到文件会永久丢失）。
func TestUnstableFilesNotMaterialized(t *testing.T) {
	root := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	// 文件写入后立刻采集：mtime 在稳定窗口内。
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	// 源的稳定窗口 30s：文件刚写入，List 报 ErrFileUnstable。
	src := e.Source.(*source.Source)
	src.StableWindow = 30 * time.Second
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusPending {
		t.Fatalf("result = %#v, want Pending (log-only)", res)
	}
	if incomplete, _ := st.ListIncomplete(context.Background(), "prod", date(t, "2026-09-06"), 0); len(incomplete) != 0 {
		t.Fatalf("unstable date must not be materialized, got %v", incomplete)
	}
}

// 零文件探针必须清掉旧进程留下的非成功台账（Running/Failed/Pending），
// 并标记 Dropped 让观察层退役 UI 行；Succeeded 是历史事实，保留。
func TestZeroFileProbeDropsStaleLedger(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	key := model.CollectionKey{SourceID: "prod", Date: date(t, "2026-09-07")}

	cases := []struct {
		name    string
		status  model.Status
		dropped bool
	}{
		{name: "running", status: model.StatusRunning, dropped: true},
		{name: "failed", status: model.StatusFailed, dropped: true},
		{name: "succeeded", status: model.StatusSucceeded, dropped: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := state.NewMemory()
			if ok, err := st.Begin(ctx, key, time.Hour); err != nil || !ok {
				t.Fatalf("seed begin: ok=%v err=%v", ok, err)
			}
			if tc.status != model.StatusRunning {
				if err := st.End(ctx, key, tc.status, "seed"); err != nil {
					t.Fatal(err)
				}
			}
			e := newExecutor(t, root, st, storage.NewMemory(storage.MemoryOptions{}))
			res, err := e.Handle(ctx, model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-07"))})
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 1 || res[0].Status != model.StatusSkipped {
				t.Fatalf("result = %#v, want Skipped", res)
			}
			if res[0].Dropped != tc.dropped {
				t.Fatalf("Dropped = %v, want %v", res[0].Dropped, tc.dropped)
			}
			got, ok, _ := st.StatusOf(ctx, key)
			if tc.status == model.StatusSucceeded {
				if !ok || got != model.StatusSucceeded {
					t.Fatalf("succeeded history lost: %v (found=%v)", got, ok)
				}
			} else if ok {
				t.Fatalf("stale record %s must be dropped, got %s", tc.status, got)
			}
		})
	}
}

// 旧进程中断留下的 Running 在重探到"文件不稳定"时必须落成 Pending：
// 保持 Running 会让 UI 永远显示"采集中"，租约还会挡住后续补采。
func TestUnstableProbeConvertsStaleRunningToPending(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	ctx := context.Background()
	key := model.CollectionKey{SourceID: "prod", Date: date(t, "2026-09-06")}
	st := state.NewMemory()
	if ok, err := st.Begin(ctx, key, time.Hour); err != nil || !ok {
		t.Fatalf("seed begin: ok=%v err=%v", ok, err)
	}
	e := newExecutor(t, root, st, storage.NewMemory(storage.MemoryOptions{}))
	e.Source.(*source.Source).StableWindow = 30 * time.Second
	res, err := e.Handle(ctx, model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusPending {
		t.Fatalf("result = %#v, want Pending", res)
	}
	got, ok, _ := st.StatusOf(ctx, key)
	if !ok || got != model.StatusPending {
		t.Fatalf("stale running = %v (found=%v), want Pending", got, ok)
	}
	incomplete, _ := st.ListIncomplete(ctx, "prod", key.Date, 0)
	if len(incomplete) != 1 {
		t.Fatalf("pending date must stay listed for recovery, got %v", incomplete)
	}
}

func TestCollectorStorageFailureIsolation(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{OnWrite: func(context.Context, model.Batch, int64) error {
		return errors.New("simulated storage failure")
	}})
	e := newExecutor(t, root, st, mem)
	_, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil || !strings.Contains(err.Error(), "simulated storage failure") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectorMetadataPropagatesToBatchesAndResult(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "orders.csv", "id,name\n1,a\n2,b\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	ex, err := appmetadata.NewExtractor(appmetadata.SourceRuleSet{SourceID: "prod", Root: "", Rules: []appmetadata.Rule{
		{Name: "file", From: appmetadata.SourceFilename, Pattern: "{file}.csv", Required: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.MetadataExtractor = ex
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Files) != 1 {
		t.Fatalf("unexpected result: %#v", res)
	}
	fr := res[0].Files[0]
	if got, _ := fr.Metadata.Get("file"); got != "orders" {
		t.Fatalf("FileResult metadata file = %q, want orders", got)
	}
	batches := mem.Batches()
	if len(batches) == 0 {
		t.Fatal("no stored batches")
	}
	for _, b := range batches {
		got, ok := b.Metadata.Get("file")
		if !ok || got != "orders" {
			t.Fatalf("batch metadata file = %q (present=%v), want orders", got, ok)
		}
	}
}

func TestCollectorMetadataErrorIsFileLevelFailure(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	writeDateCSV(t, root, "2026-09-06", "order-7.csv", "id,name\n7,o\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	ex, err := appmetadata.NewExtractor(appmetadata.SourceRuleSet{SourceID: "prod", Root: "", Rules: []appmetadata.Rule{
		{Name: "id", From: appmetadata.SourceFilename, Pattern: "order-{id}.csv", Required: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.MetadataExtractor = ex
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("metadata mismatch for a.csv must fail the collection run")
	}
	if mem.Total() != 1 {
		t.Fatalf("rows = %d, want 1 (only order-7.csv)", mem.Total())
	}
	// The failed file is left incomplete so a corrected retry can proceed;
	// the successful file was not duplicated.
	_, err = e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("retry must still report a.csv")
	}
	if mem.Total() != 1 {
		t.Fatalf("retry duplicated rows: total=%d", mem.Total())
	}
}

func TestCollectorFailureLedgerRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n")
	st := state.NewMemory()
	// The sink fails on the first pass and recovers for the retry, mimicking a
	// remote database outage between two scheduler triggers.
	mem := storage.NewMemory(storage.MemoryOptions{OnWrite: func(context.Context, model.Batch, int64) error {
		return errors.New("remote database down")
	}})
	e := newExecutor(t, root, st, mem)
	if _, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "pass1", Date: ptr(date(t, "2026-09-06"))}); err == nil {
		t.Fatal("first pass must fail")
	}
	failures, err := st.ListFileFailures(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].File.Name != "a.csv" {
		t.Fatalf("ledger = %#v, want a.csv", failures)
	}
	if failures[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (both rows share one batch)", failures[0].Attempts)
	}

	// The database is back: the retry completes and retires the ledger entry.
	mem2 := storage.NewMemory(storage.MemoryOptions{})
	e2 := newExecutor(t, root, st, mem2)
	if _, err := e2.Handle(context.Background(), model.CollectionRequested{Reason: "pass2", Date: ptr(date(t, "2026-09-06"))}); err != nil {
		t.Fatal(err)
	}
	if mem2.Total() != 2 {
		t.Fatalf("rows after retry = %d, want 2", mem2.Total())
	}
	gone, err := st.ListFileFailures(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("ledger after successful retry = %#v, want empty", gone)
	}
}

func TestCollectorParseFailureIsRecordedInLedger(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	writeDateCSV(t, root, "2026-09-06", "broken.csv", "id,name\n\"bad,2\n")
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	if _, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))}); err == nil {
		t.Fatal("malformed file must fail the run")
	}
	failures, err := st.ListFileFailures(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].File.Name != "broken.csv" {
		t.Fatalf("ledger = %#v, want broken.csv only", failures)
	}
}

// recordingObserver 按到达顺序记录即时事件（并发安全，供 -race 验证）。
type recordingObserver struct {
	mu     sync.Mutex
	events []string
}

func (o *recordingObserver) add(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, s)
}

func (o *recordingObserver) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func (o *recordingObserver) CollectionStarted(_ context.Context, key model.CollectionKey) {
	o.add("started:" + key.Date.String())
}
func (o *recordingObserver) FileStarted(_ context.Context, _ model.CollectionKey, file model.FileIdentity) {
	o.add("file:" + file.Name)
}
func (o *recordingObserver) FileDone(_ context.Context, _ model.CollectionKey, fr *model.FileResult) {
	o.add("done:" + fr.File.Name + ":" + string(fr.Status))
}
func (o *recordingObserver) CollectionDone(_ context.Context, res *model.CollectionResult) {
	o.add("done:" + res.Key.Date.String() + ":" + string(res.Status))
}

func TestExecutorParallelFilesWithBackupAndImmediateEvents(t *testing.T) {
	root := t.TempDir()
	body := func(n int) string {
		s := "id,name\n"
		for i := 1; i <= n; i++ {
			s += fmt.Sprintf("%d,r%d\n", i, i)
		}
		return s
	}
	writeDateCSV(t, root, "2026-09-06", "a.csv", body(3))
	writeDateCSV(t, root, "2026-09-06", "b.csv", body(4))
	writeDateCSV(t, root, "2026-09-06", "c.csv", body(5))
	backup := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	obs := &recordingObserver{}
	e.Observer = obs
	e.Config.FilesPerSource = 3
	e.Config.BackupDir = backup
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSucceeded || res[0].Records != 12 {
		t.Fatalf("result = %#v", res)
	}
	for _, name := range []string{"a.csv", "b.csv", "c.csv"} {
		if _, err := os.Stat(filepath.Join(backup, "prod", "2026-09-06", name)); err != nil {
			t.Fatalf("backup %s missing: %v", name, err)
		}
	}
	seen := map[string]int{}
	for _, ev := range obs.snapshot() {
		seen[ev]++
	}
	for _, want := range []string{
		"started:2026-09-06", "done:2026-09-06:" + string(model.StatusSucceeded),
		"file:a.csv", "file:b.csv", "file:c.csv",
		"done:a.csv:" + string(model.StatusSucceeded),
		"done:b.csv:" + string(model.StatusSucceeded),
		"done:c.csv:" + string(model.StatusSucceeded),
	} {
		if seen[want] != 1 {
			t.Fatalf("event %q count = %d, want 1 (all: %v)", want, seen[want], obs.snapshot())
		}
	}
}

func TestExecutorBackupFailureFailsFile(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n")
	// backup_dir 指向一个普通文件：MkdirAll 必然失败。
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	e.Config.BackupDir = filepath.Join(blocked, "sub")
	_, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(date(t, "2026-09-06"))})
	if err == nil {
		t.Fatal("backup failure must fail the run (strict semantics)")
	}
	if mem.Total() != 1 {
		t.Fatalf("rows = %d, want 1 (write precedes backup)", mem.Total())
	}
	failures, ferr := st.ListFileFailures(context.Background(), "prod")
	if ferr != nil {
		t.Fatal(ferr)
	}
	if len(failures) != 1 || failures[0].File.Name != "a.csv" || !strings.Contains(failures[0].Error, "backup") {
		t.Fatalf("ledger = %#v, want a.csv backup failure", failures)
	}
}

// 写库在文件中途失败：已提交的前置批次是合法的部分数据，但文件不能被
// 标记完成；下一轮从头重读，已写批次幂等跳过、剩余批次补齐，总行数不丢
// 不重。这是"丢数据不能忍"在调度层的断言。
func TestCollectorMidFileWriteFailureRetriesWithoutLoss(t *testing.T) {
	root := t.TempDir()
	writeDateCSV(t, root, "2026-09-06", "a.csv", "id,name\n1,a\n2,b\n3,c\n4,d\n5,e\n")
	st := state.NewMemory()
	// 第一批 2 行落库，第二批注入失败（模拟远程库中途断开）。
	mem := storage.NewMemory(storage.MemoryOptions{FailAfterBatches: 1})
	e := newExecutor(t, root, st, mem)
	if _, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "pass1", Date: ptr(date(t, "2026-09-06"))}); err == nil {
		t.Fatal("mid-file storage failure must fail the run")
	}
	if got := mem.Total(); got != 2 {
		t.Fatalf("rows after partial write = %d, want 2 (only the first batch)", got)
	}
	failures, err := st.ListFileFailures(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].File.Name != "a.csv" {
		t.Fatalf("failure ledger = %#v, want a.csv", failures)
	}

	// 库恢复：从头重读，前 2 行幂等跳过，后 3 行补齐。
	mem2 := storage.NewMemory(storage.MemoryOptions{})
	e2 := newExecutor(t, root, st, mem2)
	if _, err := e2.Handle(context.Background(), model.CollectionRequested{Reason: "pass2", Date: ptr(date(t, "2026-09-06"))}); err != nil {
		t.Fatal(err)
	}
	if got := mem2.Total(); got != 5 {
		t.Fatalf("rows after retry = %d, want 5", got)
	}
	if gone, err := st.ListFileFailures(context.Background(), "prod"); err != nil || len(gone) != 0 {
		t.Fatalf("failure ledger after successful retry = %#v, err=%v", gone, err)
	}

	// 再全量重放一次（模拟状态文件丢失后重读）：幂等仍然是 5 行。
	mem3 := storage.NewMemory(storage.MemoryOptions{})
	e3 := newExecutor(t, root, state.NewMemory(), mem3)
	if _, err := e3.Handle(context.Background(), model.CollectionRequested{Reason: "pass3", Date: ptr(date(t, "2026-09-06"))}); err != nil {
		t.Fatal(err)
	}
	if got := mem3.Total(); got != 5 {
		t.Fatalf("rows after full replay = %d, want 5", got)
	}
}
