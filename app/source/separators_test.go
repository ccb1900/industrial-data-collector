package source

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// 配置里的反斜杠一律视为分隔符，统一为主机原生风格后，任何主机上
// 组合出的路径都只含一种分隔符，不会出现 \\host\logs/202609 的混搭。
func TestNativeSepUnifiesSeparators(t *testing.T) {
	cases := []struct{ in, wantUnix, wantWindows string }{
		{`\\192.168.1.100\logs`, `//192.168.1.100/logs`, `\\192.168.1.100\logs`},
		{`C:\data\plant`, `C:/data/plant`, `C:\data\plant`},
		{`/mnt/logs`, `/mnt/logs`, `\mnt\logs`},
		{`a_YYYYMMDD.log`, `a_YYYYMMDD.log`, `a_YYYYMMDD.log`},
		{"", "", ""},
	}
	for _, c := range cases {
		got := nativeSep(c.in)
		want := c.wantUnix
		if runtime.GOOS == "windows" {
			want = c.wantWindows
		}
		if got != want {
			t.Errorf("nativeSep(%q) = %q, want %q", c.in, got, want)
		}
		if strings.Contains(got, "/") && strings.Contains(got, `\`) {
			t.Errorf("nativeSep(%q) = %q mixes separators", c.in, got)
		}
	}
}

// 端到端：根用 Windows 风格书写（部署在 Windows 上的真实写法），
// mac/Linux 主机上必须仍能定位月份目录 + 文件名内嵌日期的文件，
// 而不是把混搭路径当成"文件不存在"。
func TestWindowsStyleRootFindsDatedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "202609"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "202609", "a_20260908.log"),
		[]byte("col1,col2\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 把真实存在的根改写成反斜杠风格，模拟跨平台书写。
	winRoot := strings.ReplaceAll(root, "/", `\`)

	s := New("mt-001", winRoot, "", 0)
	s.DateDirLayout = "YYYYMM"
	s.FilenameDateLayout = "a_YYYYMMDD.log"

	// Root() 的消费方（path-metadata 相对路径推导）拿到的是统一后的值。
	if strings.Contains(s.Root(), `\`) == (runtime.GOOS != "windows") {
		t.Fatalf("Root() not normalized for host: %q", s.Root())
	}

	date := model.NewCollectionDate(time.Date(2026, 9, 8, 0, 0, 0, 0, time.Local))
	files, err := s.List(context.Background(), model.ListRequest{Date: date})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("List found %d file(s), want 1", len(files))
	}
	if files[0].Name != "a_20260908.log" {
		t.Fatalf("found %q, want a_20260908.log", files[0].Name)
	}
}

// 日期子目录布局同理：月份目录 + 目录遍历发现。
func TestWindowsStyleRootFindsMonthDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "202609"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "202609", "b.csv"), []byte("a\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	winRoot := strings.ReplaceAll(root, "/", `\`)

	s := New("mt-001", winRoot, "*.csv", 0)
	s.DateDirLayout = "YYYYMM"

	date := model.NewCollectionDate(time.Date(2026, 9, 8, 0, 0, 0, 0, time.Local))
	files, err := s.List(context.Background(), model.ListRequest{Date: date})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 1 || files[0].Name != "b.csv" {
		t.Fatalf("List = %v, want [b.csv]", files)
	}
}
