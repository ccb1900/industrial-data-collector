package state

import (
	"context"
	"path/filepath"
	"testing"
)

// "连续探不到"的计数是无数据确认（collector 的 no_data_after_days）的证据，
// 必须落在台账里：重启之后接着数，而不是从零再来一遍——否则一台每天重启的
// 采集器永远凑不满 N 次，确认机制形同虚设。

func TestNoDataStreakIsLedgerEvidence(t *testing.T) {
	for name, st := range backends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			day := key("2026-11-10")
			other := key("2026-11-11")

			if confirmed, err := st.RecordNoDataProbe(ctx, day, 2); err != nil || confirmed {
				t.Fatalf("first probe = (%v, %v), want (false, nil)", confirmed, err)
			}
			if confirmed, err := st.RecordNoDataProbe(ctx, other, 2); err != nil || confirmed {
				t.Fatalf("other date probe = (%v, %v), want (false, nil) — 计数必须按业务日分开", confirmed, err)
			}
			if confirmed, err := st.RecordNoDataProbe(ctx, day, 2); err != nil || !confirmed {
				t.Fatalf("second probe = (%v, %v), want confirmed", confirmed, err)
			}
			// 已确认后再探，仍然报告 confirmed：终态化是幂等的。
			if confirmed, err := st.RecordNoDataProbe(ctx, day, 2); err != nil || !confirmed {
				t.Fatalf("post-confirmation probe = (%v, %v), want confirmed", confirmed, err)
			}
			// 文件出现了（哪怕还没稳定）——连续性被打断。
			if err := st.ResetNoData(ctx, day); err != nil {
				t.Fatalf("reset: %v", err)
			}
			if confirmed, err := st.RecordNoDataProbe(ctx, day, 2); err != nil || confirmed {
				t.Fatalf("probe after reset = (%v, %v), want streak restarted", confirmed, err)
			}
		})
	}
}

// TestFileStateNoDataStreakSurvivesRestart 是 U2/U11 的同一课：FileState 嵌入
// MemoryState，任何新写方法忘了包 run()，证据就只活在内存里，重启清零。
func TestFileStateNoDataStreakSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	day := key("2026-11-10")
	if _, err := st.RecordNoDataProbe(ctx, day, 3); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := reopened.RecordNoDataProbe(ctx, day, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 重启后第一次探针把计数从 1 推到 2，还没到 3；但如果没落盘，这里会是
	// 1 → confirmed=false 的"永远凑不满"。
	if confirmed {
		t.Fatal("streak lost across restart — no-data evidence did not persist")
	}
	if confirmed, _ := reopened.RecordNoDataProbe(ctx, day, 3); !confirmed {
		t.Fatal("third probe must confirm after two persisted probes")
	}
}
