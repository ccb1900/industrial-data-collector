package query

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func verdict(k model.CollectionKey, name string, status model.CheckStatus, note string) model.FileCheck {
	return model.FileCheck{
		Key: k, File: file(string(k.SourceID), "/data/"+name, name),
		Status: status, Note: note, FileRows: 5, ParsedRows: 5, DBRows: 5,
		CheckedAt: time.Unix(1, 0),
	}
}

func TestReadModelChecksFromProjectionAndEvents(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	recent := key("src-a", "2026-09-06")
	older := key("src-a", "2026-09-05")

	m.AttachUnits([]UnitState{{
		SourceID: "src-a",
		Checks: []model.FileCheck{
			verdict(recent, "new.csv", model.CheckMismatch, "rows: file 5, stored 4"),
			verdict(older, "old.csv", model.CheckOK, ""),
		},
	}})

	got, err := m.ListChecks(ctx, model.CheckFilter{SourceID: "src-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].File.Name != "new.csv" {
		t.Fatalf("checks = %+v, want newest date first", got)
	}
	if got, err := m.ListChecks(ctx, model.CheckFilter{Status: model.CheckMismatch}); err != nil || len(got) != 1 {
		t.Fatalf("status filter = %+v err=%v", got, err)
	}
	if got, err := m.ListChecks(ctx, model.CheckFilter{Date: older.Date}); err != nil || len(got) != 1 || got[0].Status != model.CheckOK {
		t.Fatalf("date filter = %+v err=%v", got, err)
	}

	// 本进程的新判定覆盖台账里同文件的旧判定：一文件一条最新证据。
	m.OnChecksVerified(recent, []model.FileCheck{verdict(recent, "new.csv", model.CheckOK, "")})
	got, err = m.ListChecks(ctx, model.CheckFilter{SourceID: "src-a", Date: recent.Date})
	if err != nil || len(got) != 1 || got[0].Status != model.CheckOK {
		t.Fatalf("after re-check = %+v err=%v", got, err)
	}

	// 台账行退役时判定随行而去。
	m.OnCollectionDropped(recent)
	if got, err := m.ListChecks(ctx, model.CheckFilter{SourceID: "src-a"}); err != nil || len(got) != 1 || got[0].File.Name != "old.csv" {
		t.Fatalf("after drop = %+v err=%v", got, err)
	}
}

// TestReadModelChecksLazyHistory 证明窗口外单日回读台账：窗口里没有判定不
// 等于没有判定，那一日只是不 resident。
func TestReadModelChecksLazyHistory(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	old := key("src", "2026-01-05")
	h := &fakeHistory{
		covered: map[model.SourceID]bool{"src": true},
		checks:  map[string][]model.FileCheck{hkey(old): {verdict(old, "x.csv", model.CheckError, "unreadable")}},
	}
	m.SetHistory(h)

	got, err := m.ListChecks(ctx, model.CheckFilter{SourceID: "src", Date: old.Date})
	if err != nil || len(got) != 1 || got[0].Status != model.CheckError {
		t.Fatalf("lazy checks = %+v err=%v", got, err)
	}
	// 台账不覆盖的源（进程内状态）没有回读路径，返回窗口内的空集而不是报错。
	if got, err := m.ListChecks(ctx, model.CheckFilter{SourceID: "other", Date: old.Date}); err != nil || len(got) != 0 {
		t.Fatalf("uncovered = %+v err=%v", got, err)
	}
	// 窗口内有判定就不回读：同日的实时证据优先。
	m.OnChecksVerified(old, []model.FileCheck{verdict(old, "live.csv", model.CheckOK, "")})
	if got, err := m.ListChecks(ctx, model.CheckFilter{SourceID: "src", Date: old.Date}); err != nil || len(got) != 1 || got[0].File.Name != "live.csv" {
		t.Fatalf("in-window = %+v err=%v, window verdicts must shadow the ledger", got, err)
	}
}

func TestReadModelChecksPaging(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	k := key("src", "2026-09-06")
	m.OnChecksVerified(k, []model.FileCheck{
		verdict(k, "a.csv", model.CheckOK, ""),
		verdict(k, "b.csv", model.CheckMismatch, ""),
		verdict(k, "c.csv", model.CheckWarning, ""),
	})
	got, err := m.ListChecks(ctx, model.CheckFilter{Limit: 2, Offset: 1})
	if err != nil || len(got) != 2 || got[0].File.Name != "b.csv" || got[1].File.Name != "c.csv" {
		t.Fatalf("paged = %+v err=%v", got, err)
	}
	if got, err := m.ListChecks(ctx, model.CheckFilter{Offset: 9}); err != nil || len(got) != 0 {
		t.Fatalf("offset past end = %+v err=%v", got, err)
	}
}

// TestReadModelMismatchVisibleInCollections 钉住"不一致必须被看见"：校验永远
// 不改采集自己的状态，但它要给一次"成功却对不上"的采集留下原因，并把它送进
// 需要处理清单——否则运维界面看起来一切正常。
func TestReadModelMismatchVisibleInCollections(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	k := key("src", "2026-09-06")
	m.OnFileCompleted(k, file("src", "/s/a.csv", "a.csv"), model.NewMetadata(), 5)
	m.OnCollectionCompleted(k, time.Now())

	// 一致：计数为 0、原因保持空白、不进需要处理。
	m.OnChecksVerified(k, []model.FileCheck{verdict(k, "a.csv", model.CheckOK, "")})
	v, err := m.GetCollection(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if v.ChecksMismatch != 0 || v.Note != "" {
		t.Fatalf("ok verdict must stay silent: %+v", v)
	}
	if got, err := m.Attention(ctx); err != nil || len(got) != 0 {
		t.Fatalf("attention = %+v err=%v", got, err)
	}

	// 不一致：状态不变、计数与原因出现、进需要处理。
	m.OnChecksVerified(k, []model.FileCheck{verdict(k, "a.csv", model.CheckMismatch, "rows: file 5, stored 4")})
	v, err = m.GetCollection(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusSucceeded {
		t.Fatalf("status = %v, a check must never rewrite the collection status", v.Status)
	}
	if v.ChecksMismatch != 1 {
		t.Fatalf("checksMismatch = %d, want 1", v.ChecksMismatch)
	}
	if !strings.Contains(v.Note, "1 个文件") {
		t.Fatalf("note = %q, want the synthesized mismatch reason", v.Note)
	}
	got, err := m.Attention(ctx)
	if err != nil || len(got) != 1 || got[0].Date != "2026-09-06" {
		t.Fatalf("attention = %+v err=%v", got, err)
	}

	// 已经有原因的失败日期：不覆盖失败原文，但仍计数。
	fail := key("src", "2026-09-05")
	m.OnCollectionFailed(fail, "sink down")
	m.OnChecksVerified(fail, []model.FileCheck{verdict(fail, "b.csv", model.CheckMismatch, "")})
	vf, err := m.GetCollection(ctx, fail)
	if err != nil {
		t.Fatal(err)
	}
	if vf.Note != "sink down" || vf.ChecksMismatch != 1 {
		t.Fatalf("failed day = %+v, want the original reason kept and mismatch counted", vf)
	}
}

// TestAttentionOverlaysWindowMismatchOntoLedger 证明台账 attention（只认状态）
// 与窗口判定能合到一张清单里：窗口里的不一致日期被补上，同日期的行不重复。
func TestAttentionOverlaysWindowMismatchOntoLedger(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	mismatch := key("src", "2026-09-06")
	m.OnFileCompleted(mismatch, file("src", "/s/a.csv", "a.csv"), model.NewMetadata(), 5)
	m.OnCollectionCompleted(mismatch, time.Now())
	m.OnChecksVerified(mismatch, []model.FileCheck{verdict(mismatch, "a.csv", model.CheckMismatch, "rows")})
	failed := key("src", "2026-09-05")
	m.OnFileFailed(failed, file("src", "/s/b.csv", "b.csv"), model.NewMetadata(), 0, "boom")
	m.OnCollectionFailed(failed, "boom")

	h := &fakeHistory{
		covered: map[model.SourceID]bool{"src": true},
		// 台账只知道 2026-09-05 需要处理，2026-09-06 在它眼里是一次成功。
		attention: []CollectionView{{SourceID: "src", Date: "2026-01-05", Status: StatusPending, Note: "无数据"}},
	}
	m.SetHistory(h)

	got, err := m.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if keys := viewKeys(got); !reflect.DeepEqual(keys, []string{"src/2026-01-05", "src/2026-09-05", "src/2026-09-06"}) {
		t.Fatalf("attention = %v, want ledger row + failed day + mismatch day, no duplicates", keys)
	}
}

// TestExpiredIsNotAttentionButMismatchIs 钉住"待处理"这一列的语义边界：超期不是
// 待处理（系统不会再试，混在里面只会淹没真正看一眼的东西），校验不一致仍然是
// 待处理（数据已入库却对不上，得人去看）。投影还必须当场改口——台账说 Expired 而
// 事件驱动的内存视图还在说 Failed，等于同一件事两句话。
func TestExpiredIsNotAttentionButMismatchIs(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	expired := key("src", "2026-04-01")
	mismatch := key("src", "2026-05-01")

	m.OnCollectionFailed(expired, "device offline")
	if v, err := m.GetCollection(ctx, expired); err != nil || v.Status != StatusFailed {
		t.Fatalf("before retirement: %+v err=%v", v, err)
	}
	m.OnCollectionExpired(expired, time.Now())
	if v, err := m.GetCollection(ctx, expired); err != nil || v.Status != StatusExpired {
		t.Fatalf("after retirement: %+v err=%v", v, err)
	}

	m.OnFileCompleted(mismatch, file("src", "/s/b.csv", "b.csv"), model.NewMetadata(), 5)
	m.OnCollectionCompleted(mismatch, time.Now())
	m.OnChecksVerified(mismatch, []model.FileCheck{verdict(mismatch, "b.csv", model.CheckMismatch, "rows: file 5, stored 4")})

	views, err := m.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dates := map[string]string{}
	for _, v := range views {
		dates[v.Date] = v.Status
	}
	if _, bad := dates["2026-04-01"]; bad {
		t.Errorf("expired date must not sit in 待处理: %v", dates)
	}
	if dates["2026-05-01"] != StatusSucceeded {
		t.Errorf("a succeeded date with a row mismatch must stay actionable, got %v", dates)
	}
}
