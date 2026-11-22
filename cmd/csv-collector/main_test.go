package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/internal/parallelism"
)

// 常驻入口必须与 -once / web-ui 共用 app.Startup：它先把上一个进程留下的
// Running 行回收成可重试状态，再触发 startup 采集。旧实现只 Sync+Trigger，
// 这些孤儿日期要等满 24h 租约才会被补采，界面上则一直挂 Running。
//
// 断言的是行为而非调用记录：种子行写在 host.New 之前（StartedAt 早于进程
// 启动时刻），Startup 之后它必须已经变成 Succeeded——即被回收且当轮重采。
// 全程用状态行与在途账本同步，不靠 sleep。
func TestStartupReclaimsOrphanRunningAndReschedulesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	root := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	target := model.NewCollectionDate(time.Now().AddDate(0, 0, -1))
	dayDir := filepath.Join(root, target.String())
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dayDir, "a.csv"), []byte("id,name\n1,orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 上一进程留下的 Running 行：它在 host.New（processStart）之前就开始，
	// 是货真价实的孤儿，而不是本进程刚领走的在途作业。
	dbPath := filepath.Join(stateDir, "state.db")
	seed, err := state.OpenSQLite(dbPath, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	key := model.CollectionKey{SourceID: "prod", Date: target}
	if ok, err := seed.Begin(ctx, key, 24*time.Hour); err != nil || !ok {
		t.Fatalf("seed orphan Running: ok=%v err=%v", ok, err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "config.toml")
	config := fmt.Sprintf(`
[[components]]
id = "prod"
type = "csv-source-unit"
[components.config]
source_id = "prod"
path = %q
pattern = "*.csv"
file_stable_window_seconds = 0
state_type = "sqlite-state"
state_dir = %q
date_policy = "specific"
specific_date = %q
catchup_days = 1

[[components]]
id = "scheduler"
type = "scheduler"
[components.config]
schedule = "daily"
time = "02:00"
`, root, stateDir, target.String())
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := host.NewWatchHost(configPath, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(context.Background()) }()

	if err := app.Sync(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if err := app.Startup(ctx); err != nil {
		t.Fatalf("startup recovery: %v", err)
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 15*time.Second)
	defer waitCancel()
	if err := parallelism.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for the rescheduled pass: %v", err)
	}
	defer parallelism.DrainErrors()
	if err := parallelism.DrainErrors(); err != nil {
		t.Fatalf("rescheduled collection failed: %v", err)
	}

	readBack, err := state.OpenSQLite(dbPath, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	defer readBack.Close()
	status, existed, err := readBack.StatusOf(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !existed || status != model.StatusSucceeded {
		t.Fatalf("orphan row = (%q, existed=%v), want Succeeded: it was not reclaimed and rescheduled", status, existed)
	}
}
