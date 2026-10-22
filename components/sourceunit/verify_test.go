package sourceunit

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appbackup "gocordis-csv-collector/app/backup"
	"gocordis-csv-collector/app/collector"
	appdate "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	appparser "gocordis-csv-collector/app/parser"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/source"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
	"gocordis-csv-collector/app/verify"
)

func testDate(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// collected 用真实采集链路跑一次（临时目录 + memory sink + memory 台账），
// 返回采集结果与它的输入面，校验器面对的就是这些对象。
type collected struct {
	unit    *SourceUnitComponent
	runner  *checkRunner
	results []model.CollectionResult
	mem     *storage.MemoryStore
	st      *state.MemoryState
	root    string
	key     model.CollectionKey
}

func collectOnce(t *testing.T, body string, backupDir string) *collected {
	t.Helper()
	root := t.TempDir()
	day := "2026-09-06"
	dir := filepath.Join(root, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src := source.New("m01", root, "*.csv", 0)
	p := appparser.New()
	p.Header = true
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	key := model.CollectionKey{SourceID: "m01", Date: testDate(t, day)}
	exec := &collector.Executor{
		Source:   src,
		Parser:   p,
		Storage:  mem,
		State:    st,
		Recovery: recovery.Planner{State: st},
		Config: collector.Config{
			BatchSize:  2,
			DatePolicy: appdate.Policy{Type: appdate.PolicySpecific, Specific: key.Date},
			BackupDir:  backupDir,
		},
	}
	results, err := exec.Handle(context.Background(), model.CollectionRequested{Reason: "test", Date: &key.Date})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	unit := &SourceUnitComponent{
		sourceID:           "m01",
		path:               root,
		src:                src,
		parser:             p,
		stateSvc:           st,
		backupDir:          backupDir,
		verifyAfterCollect: true,
		logger:             testLogger(),
	}
	return &collected{unit: unit, runner: unit.newCheckRunner(mem), results: results, mem: mem, st: st, root: root, key: key}
}

func rewrite(t *testing.T, c *collected, body string) {
	t.Helper()
	path := filepath.Join(c.root, "2026-09-06", "a.csv")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRunnerRecordsOKVerdict(t *testing.T) {
	c := collectOnce(t, "id,name\n1,a\n2,b\n", "")
	if c.runner == nil {
		t.Fatal("runner must be assembled for a csv parser")
	}
	c.runner.runResults(context.Background(), c.results)
	checks, err := c.st.ListChecks(context.Background(), model.CheckFilter{SourceID: "m01"})
	if err != nil || len(checks) != 1 {
		t.Fatalf("checks = %v, err = %v", checks, err)
	}
	got := checks[0]
	if got.Status != model.CheckOK || got.FileRows != 2 || got.DBRows != 2 || got.ParsedRows != 2 {
		t.Fatalf("check = %#v", got)
	}
	if got.Key != c.key {
		t.Fatalf("check key = %v, want %v", got.Key, c.key)
	}
}

// 库比文件少一行就是不一致；台账行数与文件对不上同样是硬伤。
func TestCheckRunnerDetectsRowMismatch(t *testing.T) {
	c := collectOnce(t, "id,name\n1,a\n2,b\n", "")
	file := c.results[0].Files[0].File
	c.runner.checker.Sink = fakeSink{rows: map[string]int64{file.Identity(): 1}}
	c.runner.runResults(context.Background(), c.results)
	checks, _ := c.st.ListChecks(context.Background(), model.CheckFilter{SourceID: "m01"})
	if len(checks) != 1 {
		t.Fatalf("checks = %v", checks)
	}
	got := checks[0]
	if got.Status != model.CheckMismatch {
		t.Fatalf("status = %s, note = %q", got.Status, got.Note)
	}
	if got.FileRows != 2 || got.DBRows != 1 || got.ParsedRows != 2 {
		t.Fatalf("rows = file %d, db %d, ledger %d", got.FileRows, got.DBRows, got.ParsedRows)
	}

	// 库里行数对、台账记错了：也是硬伤（三个数必须齐平）。
	ledgerChecks := c.runner.run(context.Background(), c.key,
		[]model.FileRecordView{{Key: c.key, File: file, Records: 5}}, "manual")
	if len(ledgerChecks) != 1 || ledgerChecks[0].Status != model.CheckMismatch {
		t.Fatalf("ledger drift verdict = %v", ledgerChecks)
	}
}

// 采集之后文件被改写时，源读侧守卫把它判为 Error 而不是 Mismatch：改过的
// 文件不是这次采集的真源，不能拿它给库里的数据定罪。
func TestCheckRunnerReportsChangedFileAsError(t *testing.T) {
	c := collectOnce(t, "id,name\n1,a\n2,b\n", "")
	rewrite(t, c, "id,name\n1,a\n2,b\n3,c\n")
	c.runner.runResults(context.Background(), c.results)
	checks, _ := c.st.ListChecks(context.Background(), model.CheckFilter{SourceID: "m01"})
	if len(checks) != 1 || checks[0].Status != model.CheckError {
		t.Fatalf("checks = %v", checks)
	}
	if !strings.Contains(checks[0].Note, "在发现之后被改动") {
		t.Fatalf("note = %q", checks[0].Note)
	}
}

// 有备份就读备份：远端原文件之后被改写不能改变采集当时的判定，
// 而且读本机副本才是这条路存在的理由（不再依赖远端可用性）。
func TestCheckRunnerPrefersBackupCopy(t *testing.T) {
	backup := t.TempDir()
	c := collectOnce(t, "id,name\n1,a\n2,b\n", backup)
	// 备份已随采集落盘：确认它就在 Path 算出的位置。
	file := c.results[0].Files[0].File
	p := appbackup.Path(backup, "m01", "2026-09-06", filepath.Join(c.root, "2026-09-06"), file)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("backup copy missing at %q: %v", p, err)
	}
	rewrite(t, c, "id,name\n1,a\n2,b\n3,c\n4,d\n5,e\n")
	c.runner.runResults(context.Background(), c.results)
	checks, _ := c.st.ListChecks(context.Background(), model.CheckFilter{SourceID: "m01"})
	if len(checks) != 1 {
		t.Fatalf("checks = %v", checks)
	}
	if checks[0].Status != model.CheckOK || checks[0].FileRows != 2 {
		t.Fatalf("backup copy not preferred: %#v", checks[0])
	}
}

// 超时的那一次不落判定：半途而废的行数不是文件形态，写进台账只会误导。
func TestCheckRunnerDropsVerdictsOnTimeout(t *testing.T) {
	c := collectOnce(t, "id,name\n1,a\n2,b\n", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.runner.run(ctx, c.key, []model.FileRecordView{{Key: c.key, File: c.results[0].Files[0].File, Records: 2}}, "collect"); got != nil {
		t.Fatalf("timed-out run returned %v", got)
	}
	checks, _ := c.st.ListChecks(context.Background(), model.CheckFilter{SourceID: "m01"})
	if len(checks) != 0 {
		t.Fatalf("timed-out verdicts persisted: %v", checks)
	}
}

// 全 Skipped 的批次不进台账：结构化布局不可测是本源形态，不是每天一条
// 需要人看的记录。
func TestCheckRunnerSuppressesAllSkipped(t *testing.T) {
	c := collectOnce(t, "id,name\n1,a\n", "")
	c.runner.checker = verify.Checker{
		Shaper: appparser.New(),
		Opener: fileOpener{src: c.unit.src},
	} // Sink 缺失 → 每个文件都是 Skipped
	got := c.runner.run(context.Background(), c.key, []model.FileRecordView{{Key: c.key, File: c.results[0].Files[0].File, Records: 1}}, "manual")
	if got != nil {
		t.Fatalf("skipped batch must yield no verdicts, got %v", got)
	}
	checks, _ := c.st.ListChecks(context.Background(), model.CheckFilter{})
	if len(checks) != 0 {
		t.Fatalf("skipped rows persisted: %v", checks)
	}
}

func TestNewCheckRunnerOffAndUnmeasurable(t *testing.T) {
	st := state.NewMemory()
	mem := storage.NewMemory(storage.MemoryOptions{})
	unit := &SourceUnitComponent{sourceID: "m01", parser: appparser.New(), stateSvc: st, logger: testLogger()}
	if unit.newCheckRunner(mem) != nil {
		t.Fatal("verify_after_collect=false must yield no runner")
	}
	unit.verifyAfterCollect = true
	unit.parser = dumbParser{}
	if unit.newCheckRunner(mem) != nil {
		t.Fatal("a parser without row/column re-measurement must yield no runner")
	}
	if unit.newCheckRunner(noCountSink{}) != nil {
		t.Fatal("a sink without row counts must yield no runner")
	}
}

// VerifyNow 是控制台手动重校验的入口：没有活动执行体、或该日没有已完成
// 文件时，必须给出可判别的原因，而不是返回一个空列表假装校验过。
func TestVerifyNowPaths(t *testing.T) {
	c := collectOnce(t, "id,name\n1,a\n2,b\n", "")
	if _, err := c.unit.VerifyNow(context.Background(), c.key); err == nil {
		t.Fatal("no active runner must report unavailable")
	} else if !errs.Is(err, errs.ErrDependency) {
		t.Fatalf("err = %v", err)
	}
	c.unit.setActive(c.runner)
	if _, err := c.unit.VerifyNow(context.Background(), model.CollectionKey{SourceID: "m01", Date: testDate(t, "2026-01-02")}); !errs.Is(err, errs.ErrNotFound) {
		t.Fatalf("empty date err = %v", err)
	}
	checks, err := c.unit.VerifyNow(context.Background(), c.key)
	if err != nil || len(checks) != 1 || checks[0].Status != model.CheckOK {
		t.Fatalf("checks = %v, err = %v", checks, err)
	}
	persisted, _ := c.st.ListChecks(context.Background(), model.CheckFilter{SourceID: "m01"})
	if len(persisted) != 1 || persisted[0].CheckedAt.IsZero() {
		t.Fatalf("manual verdict not persisted: %v", persisted)
	}
	if !c.unit.VerificationActive() {
		t.Fatal("active runner must report verification available")
	}
}

// 校验用的路径算式必须与备份写入端一致，否则副本永远找不到、静默回源。
func TestFileOpenerBackupFirst(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026-09-06")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte("id\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := source.New("m01", root, "*.csv", 0)
	key := model.CollectionKey{SourceID: "m01", Date: testDate(t, "2026-09-06")}

	opener := fileOpener{src: src, backupDir: t.TempDir()}
	files, err := src.List(context.Background(), model.ListRequest{SourceID: "m01", Date: key.Date})
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
	file := files[0]
	rc, err := opener.Open(context.Background(), key, file)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "id\n1\n" {
		t.Fatalf("missing backup must fall back to the source file, read %q", body)
	}

	if err := appbackup.File(opener.backupDir, "m01", "2026-09-06", dir, file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Path, []byte("id\n1\n2\n3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err = opener.Open(context.Background(), key, file)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(rc)
	rc.Close()
	if string(body) != "id\n1\n" {
		t.Fatalf("backup copy must win over the rewritten source, read %q", body)
	}
}

type dumbParser struct{}

// fakeSink 报告一个可控的库侧行数：校验的接线（判定→台账→事件）需要
// 能构造"库比文件少一行"，真实计数 SQL 在 app/storage 的 checks 测试覆盖。
type fakeSink struct{ rows map[string]int64 }

func (f fakeSink) CountFileRows(context.Context, model.CollectionKey) (map[string]int64, error) {
	return f.rows, nil
}

func (f fakeSink) ColumnCoverage([]string) model.ColumnCoverage {
	return model.ColumnCoverage{Stored: 2, LossAllowed: true}
}

func (dumbParser) Parse(context.Context, io.Reader) (model.CSVDocument, error) {
	return model.CSVDocument{}, nil
}

// noCountSink 是一个能写但不能计数的数据面（校验器无法据此判定）。
type noCountSink struct{ storage *storage.MemoryStore }

func (n noCountSink) Write(ctx context.Context, b model.Batch) error { return n.storage.Write(ctx, b) }
func (n noCountSink) Close() error                                   { return n.storage.Close() }
