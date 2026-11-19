package collector

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/parser"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/app/source"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/storage"
)

func benchParser() *parser.Parser {
	p := parser.New()
	p.Header = true
	return p
}

// BenchmarkNightlyWindowRescan 量的是现场最关心的那个数：30 天窗口冷启动一整轮
// 要花多久。台账每轮新建（模拟"这台机器这 30 天第一次对齐"），文件不变。
//
// 它不是门禁（时间随机器变），是一条可以反复跑的尺子：
//
//	go test ./app/collector -run '^$' -bench NightlyWindowRescan -benchtime 5x
//
// 除以 30 就是"每业务日一次的发现+入库"成本；再乘以源数就是整夜预算。
// 用本地 temp 目录与内存 sink，不碰任何真实数据库。
func BenchmarkNightlyWindowRescan(b *testing.B) {
	root := filepath.Join(b.TempDir(), "root")
	for i := 0; i < 30; i++ {
		day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
		dir := filepath.Join(root, day)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		var sb strings.Builder
		sb.WriteString("id,name\n")
		for r := 1; r <= 50; r++ {
			sb.WriteString(strconv.Itoa(r))
			sb.WriteString(",x\n")
		}
		body := sb.String()
		if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	now := func() time.Time { return time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC) }
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		st := state.NewMemory()
		e := &Executor{
			Source:   source.New("prod", root, "*.csv", 0),
			Parser:   benchParser(),
			Storage:  storage.NewMemory(storage.MemoryOptions{}),
			State:    st,
			Recovery: recovery.Planner{State: st, CatchupDays: 30, Now: now},
			Config: Config{
				BatchSize:  1000,
				Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
				Now:        now,
				DatePolicy: datepolicy.Policy{Type: datepolicy.PolicyYesterday, Now: now},
				Boundary:   datepolicy.Boundary{Now: now, WindowDays: 30},
			},
		}
		b.StartTimer()
		if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNightlyWindowRescanSQLiteState 是同一条尺子的现网形态：
// laser_prod 用的就是 sqlite-state，内存台账那条数只会偏乐观。
// sink 仍是内存的——刻意不连任何真实数据库，量的是发现 + 台账 + 解析入库的
// 结构性成本，不是某个现网实例的 IO。
func BenchmarkNightlyWindowRescanSQLiteState(b *testing.B) {
	dir := b.TempDir()
	root := filepath.Join(dir, "root")
	for i := 0; i < 30; i++ {
		day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
		mk := filepath.Join(root, day)
		if err := os.MkdirAll(mk, 0o755); err != nil {
			b.Fatal(err)
		}
		var sb strings.Builder
		sb.WriteString("id,name\n")
		for r := 1; r <= 50; r++ {
			sb.WriteString(strconv.Itoa(r))
			sb.WriteString(",x\n")
		}
		if err := os.WriteFile(filepath.Join(mk, "a.csv"), []byte(sb.String()), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	now := func() time.Time { return time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	db := filepath.Join(dir, "state", "state.db")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		st, err := state.OpenSQLite(db, "prod", "")
		if err != nil {
			b.Fatal(err)
		}
		e := &Executor{
			Source:   source.New("prod", root, "*.csv", 0),
			Parser:   benchParser(),
			Storage:  storage.NewMemory(storage.MemoryOptions{}),
			State:    st,
			Recovery: recovery.Planner{State: st, CatchupDays: 30, Now: now},
			Config: Config{
				BatchSize:  1000,
				Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
				Now:        now,
				DatePolicy: datepolicy.Policy{Type: datepolicy.PolicyYesterday, Now: now},
				Boundary:   datepolicy.Boundary{Now: now, WindowDays: 30},
			},
		}
		b.StartTimer()
		if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		st.Close()
		// 每轮回到冷启动：同一个 db 里留着上一轮的成功记录，测的就不是首轮了。
		if err := os.Remove(db); err != nil && !os.IsNotExist(err) {
			b.Fatal(err)
		}
	}
}

// BenchmarkWindowRescanAllDatesMissing 量的是最难受的那一晚：整夜重扫 30 个
// 日期，一个文件都不在（停机、假期、共享盘挂掉但可达）。这些日期不落台账，
// 所以每晚都会重探——成本全部来自文件系统往返。本地盘上的这个数只是下界：
// 现网是 SMB，一次 stat 通常按毫秒计，乘以 源数 × 30 就是整夜的探测预算。
func BenchmarkWindowRescanAllDatesMissing(b *testing.B) {
	root := filepath.Join(b.TempDir(), "empty")
	if err := os.MkdirAll(root, 0o755); err != nil {
		b.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	st := state.NewMemory()
	e := &Executor{
		Source:   source.New("prod", root, "*.csv", 0),
		Parser:   benchParser(),
		Storage:  storage.NewMemory(storage.MemoryOptions{}),
		State:    st,
		Recovery: recovery.Planner{State: st, CatchupDays: 30, Now: now},
		Config: Config{
			BatchSize:  1000,
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:        now,
			DatePolicy: datepolicy.Policy{Type: datepolicy.PolicyYesterday, Now: now},
			Boundary:   datepolicy.Boundary{Now: now, WindowDays: 30},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Handle(ctx, model.CollectionRequested{Reason: "scheduled"}); err != nil {
			b.Fatal(err)
		}
	}
}
