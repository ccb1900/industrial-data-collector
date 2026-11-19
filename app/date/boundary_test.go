package date

import (
	"testing"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

func mustDate(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

// frozen 把时钟钉死在某个瞬间：日期策略的正确性只能这样验，不能等自然日切换。
func frozen(s string) func() time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

func TestBoundaryRejectsFutureDatesOnEveryPath(t *testing.T) {
	b := Boundary{Now: frozen("2026-11-19T02:00:00Z"), WindowDays: 30}
	for _, day := range []string{"2026-11-20", "2027-03-01"} {
		if err := b.Check(mustDate(t, day)); !errs.Is(err, errs.ErrDateBoundary) {
			t.Errorf("future %s must be rejected, got %v", day, err)
		}
	}
}

func TestBoundaryTodayNeedsAnExplicitSameDaySource(t *testing.T) {
	today := mustDate(t, "2026-11-19")
	if err := (Boundary{Now: frozen("2026-11-19T02:00:00Z"), WindowDays: 30}).Check(today); !errs.Is(err, errs.ErrDateBoundary) {
		t.Errorf("dated source must never collect today, got %v", err)
	}
	sameDay := Boundary{Now: frozen("2026-11-19T02:00:00Z"), WindowDays: 30, SameDay: true}
	if err := sameDay.Check(today); err != nil {
		t.Errorf("flat/watch export is today by nature: %v", err)
	}
	// 昨天的文件对当天导出的源仍然合法。
	if err := sameDay.Check(mustDate(t, "2026-11-18")); err != nil {
		t.Errorf("yesterday must stay collectable: %v", err)
	}
}

// TestBoundaryAnchorsTheWindowOnCutoff 钉住窗口的算法本身（planner 与它同源）：
// cutoff=11-18、30 天含首日 → 起点 10-20。窗口是"排哪些天"的范围，不是"能不能
// 采"的条件。
func TestBoundaryAnchorsTheWindowOnCutoff(t *testing.T) {
	b := Boundary{Now: frozen("2026-11-19T02:00:00Z"), WindowDays: 30}
	start, ok := b.Start()
	if !ok || start.String() != "2026-10-20" {
		t.Fatalf("window start = %v (%v), want 2026-10-20", start, ok)
	}
	// 窗口之前的日期不因此被拒：回填一个半年前的业务日是停机恢复后唯一的通道。
	for _, day := range []string{"2026-10-20", "2026-05-02", "2025-11-19"} {
		if err := b.Check(mustDate(t, day)); err != nil {
			t.Errorf("%s must stay collectable (window bounds the plan, not the gate): %v", day, err)
		}
	}
	if got, ok := (Boundary{Now: frozen("2026-11-19T02:00:00Z")}).Start(); ok || !got.IsZero() {
		t.Error("WindowDays<=0 must have no lower bound at all (legacy deployments)")
	}
}

// TestBoundaryAcrossCalendarEdges 是时间冻结测试：月末、年末、闰年、午夜边界。
// 这些位置 AddDate 的进位规则和目录命名（{YYYYMM}）最容易一起出错。
func TestBoundaryAcrossCalendarEdges(t *testing.T) {
	cases := []struct {
		now          string
		windowDays   int
		wantCutoff   string
		wantStart    string
		legalTarget  string
		illegalAfter string
	}{
		{"2026-03-01T00:30:00Z", 30, "2026-02-28", "2026-01-30", "2026-01-30", "2026-03-01"}, // 平年 2 月末
		{"2028-03-01T00:30:00Z", 30, "2028-02-29", "2028-01-31", "2028-02-29", "2028-03-01"}, // 闰年 2/29
		{"2027-01-01T00:05:00Z", 30, "2026-12-31", "2026-12-02", "2026-12-02", "2027-01-02"}, // 跨年
		{"2026-11-19T23:59:59Z", 1, "2026-11-18", "2026-11-18", "2026-11-18", "2026-11-19"},  // 窗口=1 天
	}
	for _, c := range cases {
		b := Boundary{Now: frozen(c.now), WindowDays: c.windowDays}
		if got := b.Cutoff().String(); got != c.wantCutoff {
			t.Errorf("now=%s cutoff=%s, want %s", c.now, got, c.wantCutoff)
		}
		start, ok := b.Start()
		if !ok || start.String() != c.wantStart {
			t.Errorf("now=%s window start=%v (%v), want %s", c.now, start, ok, c.wantStart)
		}
		if err := b.Check(mustDate(t, c.legalTarget)); err != nil {
			t.Errorf("now=%s legal target %s refused: %v", c.now, c.legalTarget, err)
		}
		if err := b.Check(mustDate(t, c.illegalAfter)); !errs.Is(err, errs.ErrDateBoundary) {
			t.Errorf("now=%s must refuse %s, got %v", c.now, c.illegalAfter, err)
		}
	}
}
