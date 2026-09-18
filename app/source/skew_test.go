package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// 文件服务器时钟快于采集机（工厂现场常态）：刚写完的文件 mtime 落在未来，
// now.Sub(mtime) 为负——负差值恒小于稳定窗口，老实现把文件永远挡在发现
// 之外，源卡死在 Pending。mtime 在未来必须直接放行（写入中的文件由双
// stat 兜住）。本地盘 + NTP 的开发机永不复现，UNC 生产必踩。
func TestFutureModTimeNotUnstable(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026-09-01")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "a_260901.log")
	if err := os.WriteFile(path, []byte("id,val\n1,10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(45 * time.Minute) // 服务器时钟快 45 分钟
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	s := New("m", root, "*.log", 5*time.Minute)
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-01")); err != nil {
		t.Fatal(err)
	}
	files, err := s.List(context.Background(), model.ListRequest{Date: d})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 1 || files[0].Name != "a_260901.log" {
		t.Fatalf("List = %+v, want [a_260901.log]", files)
	}
}
