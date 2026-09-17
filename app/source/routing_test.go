package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// 布局：\\机台\logs\202609\a_20260908.log —— 月份目录 + 文件名内嵌日期。
// date_dir_layout=200601 + filename_date_layout=a_20060102.log 双键路由。
func TestDateDirAndFilenameRouting(t *testing.T) {
	root := t.TempDir()
	month := filepath.Join(root, "202609")
	if err := os.MkdirAll(month, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(month, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a_20260908.log", "id,val\n1,10\n")
	write("a_20260909.log", "id,val\n2,20\n")
	// 同目录下其他格式/其他机台的文件：路由必须只认自己的模板。
	write("b_20260908.log", "other\n")

	src := &Source{
		SourceID: "machine-a", root: root,
		Pattern:       "a_*.log",
		DateDirLayout: "YYYYMM", FilenameDateLayout: "a_YYYYMMDD.log",
	}
	date := func(s string) model.CollectionDate {
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(s)); err != nil {
			t.Fatal(err)
		}
		return d
	}

	res, err := src.List(context.Background(), model.ListRequest{SourceID: "machine-a", Date: date("2026-09-08")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != "a_20260908.log" {
		t.Fatalf("08日 = %+v", res)
	}
	if !filepath.IsAbs(res[0].Path) && filepath.Dir(res[0].Path) != month {
		t.Fatalf("path = %q, want under %s", res[0].Path, month)
	}

	res, err = src.List(context.Background(), model.ListRequest{SourceID: "machine-a", Date: date("2026-09-09")})
	if err != nil || len(res) != 1 || res[0].Name != "a_20260909.log" {
		t.Fatalf("09日 = %+v err=%v", res, err)
	}
}

// 路由日期缺失：过期日分类为 NotFound（终态无数据），当天为可重试。
func TestDateRoutingMissingClassified(t *testing.T) {
	root := t.TempDir()
	month := filepath.Join(root, "202609")
	if err := os.MkdirAll(month, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(month, "a_20240101.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &Source{
		SourceID: "machine-a", root: root,
		DateDirLayout: "YYYYMM", FilenameDateLayout: "a_YYYYMMDD.log",
	}
	date := func(s string) model.CollectionDate {
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte(s)); err != nil {
			t.Fatal(err)
		}
		return d
	}
	// 过期日（远早于现在）目录不存在 → NotFound（executor 判 Skipped）。
	_, err := src.List(context.Background(), model.ListRequest{SourceID: "m", Date: date("2024-01-01")})
	if !errs.Is(err, errs.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// 同月但不同日的文件不被误认（路由按整个模板匹配）。
	_, err = src.List(context.Background(), model.ListRequest{SourceID: "m", Date: date("2026-09-09")})
	if !errs.Is(err, errs.ErrNotFound) {
		t.Fatalf("different day must be NotFound, got %v", err)
	}
	_ = time.Now
}

// 词表渲染：只认 YYYY/YY/MM/DD 四个记号，其余字符一律字面量——
// 尤其是设备命名习惯里的数字（m307data 的 "3"、"07"），绝不能被
// 时间布局记号吞掉重渲染。
func TestRenderDatedVocabulary(t *testing.T) {
	d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"YYYYMMDD":            "20260901",
		"YYMMDD":              "260901",
		"a_YYMMDD.log":        "a_260901.log",
		"m307data_YYMMDD.log": "m307data_260901.log",
		"YYYY":                "2026",
		"YYYYYY":              "202626",
		"MST_Jan_15_3":        "MST_Jan_15_3", // Go 布局记号词在此全是字面量
		"20260908":            "20260908",
	}
	for in, want := range cases {
		if got := renderDated(in, d); got != want {
			t.Errorf("renderDated(%q) = %q, want %q", in, got, want)
		}
	}
}

// 两位年份的文件名路由：a_260908.log ← a_YYMMDD.log。
func TestFilenameRoutingTwoDigitYear(t *testing.T) {
	root := t.TempDir()
	month := filepath.Join(root, "202609")
	if err := os.MkdirAll(month, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(month, "a_260908.log"), []byte("id\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &Source{
		SourceID: "machine-a", root: root,
		DateDirLayout: "YYYYMM", FilenameDateLayout: "a_YYMMDD.log",
	}
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-08")); err != nil {
		t.Fatal(err)
	}
	res, err := src.List(context.Background(), model.ListRequest{SourceID: "machine-a", Date: d})
	if err != nil || len(res) != 1 || res[0].Name != "a_260908.log" {
		t.Fatalf("List = %+v err=%v, want [a_260908.log]", res, err)
	}
}
