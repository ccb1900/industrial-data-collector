package query

import (
	"context"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// 概览 KPI 的口径必须覆盖投影侧：file-state 部署（以及未被 SQLite 账本覆盖的
// 源）只走内存投影，少一个分支，"超期未采"卡片就永远读 0，而台账里明明躺着
// Expired 行——2026-11-20 的现网演示实测到过这个谎。
func TestStatsCountsExpiredFromProjection(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()

	succeeded := key("src", "2026-05-01")
	m.OnFileCompleted(succeeded, file("src", "/s/a.csv", "a.csv"), model.NewMetadata(), 12)
	m.OnCollectionCompleted(succeeded, time.Now())

	failed := key("src", "2026-05-02")
	m.OnCollectionFailed(failed, "device offline")

	expired := key("src", "2026-04-01")
	m.OnCollectionFailed(expired, "share unreachable")
	m.OnCollectionExpired(expired, time.Now())

	stats, err := m.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 {
		t.Errorf("Pending = %d, want 1（只有那条 Failed 会自动重试）", stats.Pending)
	}
	if stats.Expired != 1 {
		t.Errorf("Expired = %d, want 1（Expired 单独成数，不并入 Pending）", stats.Expired)
	}
	if stats.Records != 12 {
		t.Errorf("Records = %d, want 12", stats.Records)
	}
	// 已退役的日期不许出现在"待处理"里。
	views, err := m.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Date == "2026-04-01" {
			t.Fatalf("expired date leaked into attention: %+v", views)
		}
	}
}
