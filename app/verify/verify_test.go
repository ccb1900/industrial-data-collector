package verify

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
	appparser "gocordis-csv-collector/app/parser"
)

type fakeSink struct {
	counts map[string]int64
	err    error
	cov    model.ColumnCoverage
}

func (f *fakeSink) CountFileRows(context.Context, model.CollectionKey) (map[string]int64, error) {
	return f.counts, f.err
}

func (f *fakeSink) ColumnCoverage([]string) model.ColumnCoverage { return f.cov }

type fakeOpener struct {
	body string
	err  error
}

func (o fakeOpener) Open(context.Context, model.CollectionKey, model.FileIdentity) (io.ReadCloser, error) {
	if o.err != nil {
		return nil, o.err
	}
	return io.NopCloser(strings.NewReader(o.body)), nil
}

type fakeShaper struct {
	inner      *appparser.Parser
	structured bool
}

func (s fakeShaper) Scan(ctx context.Context, r io.Reader) (appparser.Shape, error) {
	return s.inner.Scan(ctx, r)
}
func (s fakeShaper) Structured() bool    { return s.structured }
func (s fakeShaper) RaggedAllowed() bool { return s.inner.RaggedAllowed() }

func key(t *testing.T, date string) model.CollectionKey {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(date)); err != nil {
		t.Fatal(err)
	}
	return model.CollectionKey{SourceID: "src", Date: d}
}

func files(k model.CollectionKey, parsed int64, name string) ([]model.FileRecordView, model.FileIdentity) {
	file := model.FileIdentity{SourceID: k.SourceID, Path: "/share/" + name, Name: name, Size: 100}
	return []model.FileRecordView{{Key: k, File: file, Records: parsed}}, file
}

func fixedClock() time.Time { return time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC) }

func checker(body string, sink Sink, shaper Shaper) *Checker {
	return &Checker{
		Shaper: shaper,
		Sink:   sink,
		Opener: fakeOpener{body: body},
		Now:    func() time.Time { return fixedClock() },
	}
}

func parserShaper(ragged bool) fakeShaper {
	p := appparser.New()
	p.Header = true
	p.AllowRagged = ragged
	return fakeShaper{inner: p}
}

const threeRows = "a,b,c\n1,2,3\n4,5,6\n7,8,9\n"

func TestCheckOK(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, file := files(k, 3, "day.csv")
	c := checker(threeRows, &fakeSink{
		counts: map[string]int64{file.Identity(): 3},
		cov:    model.ColumnCoverage{Stored: 3},
	}, parserShaper(false))
	out := c.CheckCollection(context.Background(), k, fs)
	if len(out) != 1 {
		t.Fatalf("checks = %d", len(out))
	}
	got := out[0]
	if got.Status != model.CheckOK || got.FileRows != 3 || got.DBRows != 3 || got.StoredCols != 3 || got.Note != "" {
		t.Fatalf("got %+v", got)
	}
	if !got.CheckedAt.Equal(fixedClock()) {
		t.Fatalf("checked_at = %v", got.CheckedAt)
	}
}

func TestCheckStoredRowsMismatch(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, file := files(k, 3, "day.csv")
	c := checker(threeRows, &fakeSink{
		counts: map[string]int64{file.Identity(): 2},
		cov:    model.ColumnCoverage{Stored: 3},
	}, parserShaper(false))
	got := c.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckMismatch || !strings.Contains(got.Note, "库中 2 行") {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckLedgerRowsMismatch(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, file := files(k, 4, "day.csv")
	c := checker(threeRows, &fakeSink{
		counts: map[string]int64{file.Identity(): 3},
		cov:    model.ColumnCoverage{Stored: 3},
	}, parserShaper(false))
	got := c.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckMismatch || !strings.Contains(got.Note, "台账 4 行") {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckUnmappedColumnsStrictVsAllowed(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, file := files(k, 3, "day.csv")
	counts := map[string]int64{file.Identity(): 3}

	strict := checker(threeRows, &fakeSink{
		counts: counts,
		cov:    model.ColumnCoverage{Stored: 2, Unmapped: []string{"c"}},
	}, parserShaper(false))
	got := strict.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckMismatch || !strings.Contains(got.Note, "未入库：c") {
		t.Fatalf("strict got %+v", got)
	}

	lossy := checker(threeRows, &fakeSink{
		counts: counts,
		cov:    model.ColumnCoverage{Stored: 2, Unmapped: []string{"c"}, LossAllowed: true},
	}, parserShaper(false))
	got = lossy.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckWarning {
		t.Fatalf("lossy got %+v", got)
	}
}

func TestCheckRaggedStrictVsAllowed(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, file := files(k, 2, "day.csv")
	body := "a,b,c\n1,2,3\n4,5\n"
	counts := map[string]int64{file.Identity(): 2}

	strict := checker(body, &fakeSink{counts: counts, cov: model.ColumnCoverage{Stored: 3}}, parserShaper(false))
	got := strict.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckMismatch || got.RaggedRows != 1 {
		t.Fatalf("strict got %+v", got)
	}

	allowed := checker(body, &fakeSink{counts: counts, cov: model.ColumnCoverage{Stored: 3}}, parserShaper(true))
	got = allowed.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckWarning || got.RaggedRows != 1 {
		t.Fatalf("allowed got %+v", got)
	}
}

func TestCheckHardWinsOverSoft(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, _ := files(k, 3, "day.csv")
	c := checker(threeRows, &fakeSink{
		counts: map[string]int64{"other": 2},
		cov:    model.ColumnCoverage{Stored: 2, Unmapped: []string{"c"}, LossAllowed: true},
	}, parserShaper(false))
	got := c.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckMismatch {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got.Note, "库中 0 行") {
		t.Fatalf("missing file should count zero: %q", got.Note)
	}
}

func TestCheckErrors(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, file := files(k, 3, "day.csv")
	counts := map[string]int64{file.Identity(): 3}

	openErr := &Checker{
		Shaper: parserShaper(false),
		Sink:   &fakeSink{counts: counts, cov: model.ColumnCoverage{Stored: 3}},
		Opener: fakeOpener{err: errors.New("backup copy size differs")},
		Now:    func() time.Time { return fixedClock() },
	}
	got := openErr.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckError || !strings.Contains(got.Note, "size differs") {
		t.Fatalf("open error got %+v", got)
	}

	countErr := checker(threeRows, &fakeSink{err: errors.New("db down")}, parserShaper(false))
	got = countErr.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckError || !strings.Contains(got.Note, "db down") {
		t.Fatalf("count error got %+v", got)
	}

	badCSV := checker("a,b\n\"unclosed,2\n", &fakeSink{counts: counts, cov: model.ColumnCoverage{Stored: 2}}, parserShaper(false))
	got = badCSV.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckError {
		t.Fatalf("malformed csv got %+v", got)
	}
}

func TestCheckSkipped(t *testing.T) {
	k := key(t, "2026-10-20")
	fs, _ := files(k, 3, "day.csv")

	noSink := checker(threeRows, nil, parserShaper(false))
	got := noSink.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckSkipped {
		t.Fatalf("nil sink got %+v", got)
	}

	structured := &Checker{
		Shaper: fakeShaper{inner: appparser.New(), structured: true},
		Sink:   &fakeSink{},
		Opener: fakeOpener{body: threeRows},
		Now:    func() time.Time { return fixedClock() },
	}
	got = structured.CheckCollection(context.Background(), k, fs)[0]
	if got.Status != model.CheckSkipped || got.ParsedRows != 3 {
		t.Fatalf("structured got %+v", got)
	}

	noShaper := (&Checker{Sink: &fakeSink{}}).CheckCollection(context.Background(), k, fs)
	if noShaper[0].Status != model.CheckSkipped {
		t.Fatalf("nil shaper got %+v", noShaper[0])
	}
}

func TestSummarize(t *testing.T) {
	checks := []model.FileCheck{
		{Status: model.CheckOK},
		{Status: model.CheckMismatch},
		{Status: model.CheckMismatch},
		{Status: model.CheckWarning},
		{Status: model.CheckError},
		{Status: model.CheckSkipped},
	}
	s := Summarize(checks)
	if s.Checked != 6 || s.OK != 1 || s.Mismatch != 2 || s.Warning != 1 || s.Error != 1 || s.Skipped != 1 {
		t.Fatalf("summary = %+v", s)
	}
}
