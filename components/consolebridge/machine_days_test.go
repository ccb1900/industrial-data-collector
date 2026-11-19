package consolebridge

import (
	"context"
	"net/url"
	"testing"
	"time"

	"dynamic-runtime/extensions/console/hub"

	"gocordis-csv-collector/app/model"
)

// 机台×日汇总的行为面。健康定义是本文件的唯一口径：
// 全部格式 Succeeded 或 Skipped（含确认无数据）= 健康；
// Failed/Pending/Running/Expired 任一存在 = 不健康，且 Expired 单独成数。

func rec(src, day string, st model.Status) model.CollectionRecord {
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(day)); err != nil {
		panic(err)
	}
	return model.CollectionRecord{
		Key:    model.CollectionKey{SourceID: model.SourceID(src), Date: d},
		Status: st, StartedAt: time.Now(), EndedAt: time.Now(),
	}
}

func unit(machine, format, source string, recs ...model.CollectionRecord) MachineDayUnit {
	return MachineDayUnit{MachineID: machine, FormatID: format, SourceID: source, Records: recs}
}

func TestMachineDayRowsHealthSemantics(t *testing.T) {
	units := []MachineDayUnit{
		unit("m1", "readings", "m1-readings",
			rec("m1-readings", "2026-11-18", model.StatusSucceeded)),
		unit("m1", "alm_log", "m1-alm_log",
			rec("m1-alm_log", "2026-11-18", model.StatusFailed)),
		unit("m1", "readings", "m1-readings",
			rec("m1-readings", "2026-11-17", model.StatusSucceeded)),
		unit("m1", "alm_log", "m1-alm_log",
			rec("m1-alm_log", "2026-11-17", model.StatusSkipped)),
	}
	rows := MachineDayRows(units, MachineDayFilter{})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 machine-days", len(rows))
	}
	// 最新的一天排前面。
	if rows[0].Date != "2026-11-18" || rows[1].Date != "2026-11-17" {
		t.Fatalf("date order = %s,%s, want latest first", rows[0].Date, rows[1].Date)
	}
	// 11-18：一个成功一个失败 → 不健康，且指出是谁。
	if rows[0].Healthy {
		t.Errorf("a failed format must make the machine-day unhealthy")
	}
	if rows[0].Failed != 1 || rows[0].Succeeded != 1 {
		t.Errorf("counts = %+v, want failed=1 succeeded=1", rows[0])
	}
	if len(rows[0].Formats) != 2 || rows[0].Formats[0].Format != "alm_log" || rows[0].Formats[0].Status != string(model.StatusFailed) {
		t.Errorf("formats = %+v, want alm_log listed as Failed", rows[0].Formats)
	}
	// 11-17：成功 + 确认无数据 = 健康。
	if !rows[1].Healthy {
		t.Errorf("succeeded + confirmed no-data = healthy day, got %+v", rows[1])
	}
}

func TestMachineDayRowsExpiredIsItsOwnBucket(t *testing.T) {
	units := []MachineDayUnit{
		unit("m2", "m307", "m2-m307", rec("m2-m307", "2026-10-01", model.StatusExpired)),
	}
	rows := MachineDayRows(units, MachineDayFilter{})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Expired != 1 {
		t.Errorf("expired = %d, want 1", rows[0].Expired)
	}
	if rows[0].Healthy {
		t.Error("a day awaiting manual backfill is not healthy")
	}
	if rows[0].Failed != 0 {
		t.Errorf("expired must not be counted as failed (两种 ask 不能搅在一起): %+v", rows[0])
	}
}

func TestMachineDayRowsWithoutMachineIdentityGroupsBySource(t *testing.T) {
	units := []MachineDayUnit{
		unit("", "", "hand-written-source", rec("hand-written-source", "2026-11-18", model.StatusSucceeded)),
	}
	rows := MachineDayRows(units, MachineDayFilter{})
	if len(rows) != 1 || rows[0].Machine != "hand-written-source" {
		t.Fatalf("rows = %+v, want the source to become its own group (不丢失)", rows)
	}
	if !rows[0].Healthy {
		t.Errorf("healthy = %v, want true", rows[0].Healthy)
	}
}

func TestMachineDayRowsFiltersAndLimit(t *testing.T) {
	units := []MachineDayUnit{
		unit("m1", "r", "m1-r", rec("m1-r", "2026-11-18", model.StatusSucceeded), rec("m1-r", "2026-11-17", model.StatusSucceeded)),
		unit("m2", "r", "m2-r", rec("m2-r", "2026-11-18", model.StatusSucceeded)),
	}
	byMachine := MachineDayRows(units, MachineDayFilter{Machine: "m2"})
	if len(byMachine) != 1 || byMachine[0].Machine != "m2" {
		t.Fatalf("machine filter = %+v", byMachine)
	}
	byDate := MachineDayRows(units, MachineDayFilter{Date: "2026-11-17"})
	if len(byDate) != 1 || byDate[0].Date != "2026-11-17" {
		t.Fatalf("date filter = %+v", byDate)
	}
	limited := MachineDayRows(units, MachineDayFilter{Limit: 1})
	if len(limited) != 1 {
		t.Fatalf("limit = %d rows, want 1", len(limited))
	}
}

func TestMachineDayParamsRejectsBadLimit(t *testing.T) {
	if _, err := machineDayParams(url.Values{"limit": {"-1"}}); err == nil || err.Code != "invalid_request" {
		t.Errorf("negative limit must be invalid_request, got %v", err)
	}
	if _, err := machineDayParams(url.Values{"limit": {"abc"}}); err == nil {
		t.Error("non-numeric limit must be rejected")
	}
	if _, err := machineDayParams(url.Values{}); err != nil {
		t.Errorf("empty params must parse: %v", err)
	}
}

// fakeRegistrar 捕获注册的查询名与处理器：注册路径的行为测试，不搭真实 hub。
type fakeRegistrar struct {
	name    string
	owner   string
	handler hub.QueryHandler
}

func (f *fakeRegistrar) RegisterQuery(name, owner string, h hub.QueryHandler) (func() error, error) {
	f.name, f.owner, f.handler = name, owner, h
	return func() error { return nil }, nil
}

// TestRegisterMachineDaysWiresTheQuery：查询名、owner、参数解析、空 units 的
// 空结果——注册路径走通即可，聚合语义归纯函数的测试管。
func TestRegisterMachineDaysWiresTheQuery(t *testing.T) {
	c := &Component{}
	c.SetUnits(nil)
	fake := &fakeRegistrar{}
	cleanup, err := c.registerMachineDays(fake, "console-bridge:test")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must not be nil")
	}
	if fake.name != "machine-days" || fake.owner != "console-bridge:test" {
		t.Fatalf("registered = %s/%s", fake.name, fake.owner)
	}
	out, herr := fake.handler(context.Background(), url.Values{})
	if herr != nil || out == nil {
		t.Fatalf("handler = %v, %v", out, herr)
	}
	if _, herr := fake.handler(context.Background(), url.Values{"limit": {"x"}}); herr == nil {
		t.Error("bad limit must surface as hub error")
	}
}
