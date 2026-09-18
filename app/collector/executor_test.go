package collector

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// 巡检：expect=daily 时，回看窗口内的预期缺失生成可重试的 Failed 实例；
// 文件晚到后，下次采集成功覆盖它。
func TestInspectionCreatesRetryableFailure(t *testing.T) {
	root := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	e.Config.Expect = "daily"
	e.Config.InspectLookbackDays = 1

	// 巡检窗口相对"今天"：昨天。
	yesterday := model.NewCollectionDate(time.Now().AddDate(0, 0, -1))
	// Handle 对 Failed 结果同时返回非 nil error（与不可达路径同语义）。
	_, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(yesterday)})
	if err == nil || !strings.Contains(err.Error(), "inspection") {
		t.Fatalf("err = %v, want the inspection failure", err)
	}
	incomplete, _ := st.ListIncomplete(context.Background(), "prod", yesterday, 0)
	if len(incomplete) != 1 {
		t.Fatal("inspection failure must stay listed for retry")
	}

	// 文件晚到：下次触发采集成功，覆盖巡检失败。
	writeDateCSV(t, root, yesterday.String(), "a.csv", "id,name\n1,a\n")
	res2, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(yesterday)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 1 || res2[0].Status != model.StatusSucceeded {
		t.Fatalf("late-arrival result = %#v, want Succeeded", res2)
	}
	if incomplete2, _ := st.ListIncomplete(context.Background(), "prod", yesterday, 0); len(incomplete2) != 0 {
		t.Fatal("succeeded date must leave the retry list")
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

func TestMissingDirectoryTodayNotMaterialized(t *testing.T) {
	root := t.TempDir()
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	e := newExecutor(t, root, st, mem)
	// 实例制：今天的目录未出现 = 无证据，不物化（文件晚到后下次触发
	// 重探即得；预期缺失由巡检在窗口内负责）。
	today := model.NewCollectionDate(time.Now())
	res, err := e.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: ptr(today)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != model.StatusSkipped {
		t.Fatalf("result = %#v, want Skipped (log-only)", res)
	}
	if incomplete, _ := st.ListIncomplete(context.Background(), "prod", today, 0); len(incomplete) != 0 {
		t.Fatalf("missing date must not be materialized, got %v", incomplete)
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
	e.Config.NoDataGraceHours = 6
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
