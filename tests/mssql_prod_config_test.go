package tests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"
	toml "github.com/pelletier/go-toml/v2"

	appconfig "gocordis-csv-collector/app/config"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/sourcecomp"
	"gocordis-csv-collector/app/storage"
)

// 现网 SQL Server 配置的契约测试：configs/laser_prod_mssql.toml 里的 sink
// 与 23 个格式声明，必须在真 SQL Server 上逐条建表并写入成功。方言分支的
// 正确性只有这么验才算数——声明列与实建列对不上、键列超宽、保留字撞名，
// 都在这一步暴露，而不是等现场第一批数据。
//
// 两个层次：形状检查（TestProdMSSQLConfigShape）永远跑，只需配置文件本身；
// 真库检查（TestProdMSSQLConfigRealServer）仅在 REAL_SQLSERVER_DSN 存在时
// 跑，凭据不落盘。每次运行生成带时间戳的独立表，跑完 DROP，不碰既有对象。

const mssqlConfigFile = "laser_prod_mssql.toml"

type mssqlDoc struct {
	Sinks []struct {
		Name      string `toml:"name"`
		Driver    string `toml:"driver"`
		DSN       string `toml:"dsn"`
		FileTable string `toml:"file_table"`
		FileKey   bool   `toml:"file_key"`
	} `toml:"sinks"`
	FormatGroups []struct {
		Name    string   `toml:"name"`
		Formats []string `toml:"formats"`
	} `toml:"format_groups"`
	Formats []struct {
		Name    string                  `toml:"name"`
		Match   string                  `toml:"match"`
		Table   string                  `toml:"table"`
		Sink    string                  `toml:"sink"`
		Columns []storage.ColumnMapping `toml:"columns"`
	} `toml:"formats"`
}

func loadMSSQLDoc(t *testing.T) (mssqlDoc, []byte) {
	t.Helper()
	path := filepath.Join("..", "configs", mssqlConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc mssqlDoc
	if err := toml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode %s: %v", mssqlConfigFile, err)
	}
	return doc, data
}

func TestProdMSSQLConfigShape(t *testing.T) {
	doc, data := loadMSSQLDoc(t)

	parsed, err := sourcecomp.Expand(data)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if err := appconfig.Validate(parsed); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if len(doc.Sinks) != 1 {
		t.Fatalf("sinks = %d, want 1（这份配置只该有一个落库目标）", len(doc.Sinks))
	}
	sink := doc.Sinks[0]
	if sink.Driver != "sqlserver" {
		t.Errorf("sink %q driver = %q, want sqlserver", sink.Name, sink.Driver)
	}
	if !sink.FileKey {
		t.Errorf("sink %q file_key = false, want true（新库上线没有迁移负担，该用省体积的骨架）", sink.Name)
	}
	if sink.FileTable == "" {
		t.Errorf("sink %q file_table empty — file_key 模式必需注册表", sink.Name)
	}
	// 提交进仓库的配置里不能有真凭据：dsn 必须是占位符形态。
	if !strings.Contains(sink.DSN, "<密码>") || !strings.Contains(sink.DSN, "<数据库主机>") {
		t.Errorf("dsn 应保留占位符（密码/主机在部署机上填写），got %q", sink.DSN)
	}

	if len(doc.Formats) == 0 {
		t.Fatal("no formats declared")
	}
	byName := map[string]int{}
	tables := map[string]bool{}
	for _, f := range doc.Formats {
		byName[f.Name] = len(f.Columns)
		if f.Sink != sink.Name {
			t.Errorf("format %q references sink %q, want %q", f.Name, f.Sink, sink.Name)
		}
		if len(f.Columns) == 0 {
			t.Errorf("format %q has no columns", f.Name)
		}
		if tables[f.Table] {
			t.Errorf("duplicate table %q (format %q)", f.Table, f.Name)
		}
		tables[f.Table] = true
		// 列名必须是简单标识符：非字母数字会被 quoteIdent 拒掉，落库时才报错。
		for _, c := range f.Columns {
			if !simpleIdent(c.Column) {
				t.Errorf("format %q column %q is not a simple identifier", f.Name, c.Column)
			}
		}
	}
	for _, g := range doc.FormatGroups {
		for _, name := range g.Formats {
			if _, ok := byName[name]; !ok {
				t.Errorf("format group %q references unknown format %q", g.Name, name)
			}
		}
	}
	if len(doc.FormatGroups) != 3 {
		t.Errorf("format groups = %d, want 3（sanling3/5/6）", len(doc.FormatGroups))
	}
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func simpleIdent(s string) bool { return identRe.MatchString(s) }

func TestProdMSSQLConfigRealServer(t *testing.T) {
	dsn := os.Getenv("REAL_SQLSERVER_DSN")
	if dsn == "" {
		t.Skip("REAL_SQLSERVER_DSN not set")
	}
	doc, _ := loadMSSQLDoc(t)
	sink := doc.Sinks[0]

	// 一个注册表 + 每格式一张数据表：与现网同形状（23 张表共用同一条发号
	// 计数器），而不是每个格式各建一套。
	stamp := strconv.FormatInt(time.Now().UnixNano()/1e6, 10)
	registry := "zcfg_files_" + stamp
	tableNames := make([]string, 0, len(doc.Formats))
	for _, f := range doc.Formats {
		tableNames = append(tableNames, "zcfg_"+f.Name+"_"+stamp)
	}
	ctx := context.Background()
	dropAll := func() {
		db, err := sql.Open("sqlserver", dsn)
		if err != nil {
			t.Logf("cleanup open: %v", err)
			return
		}
		defer db.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, tbl := range append([]string{registry + "_file_key_seq", registry}, tableNames...) {
			if _, err := db.ExecContext(cctx, "DROP TABLE IF EXISTS ["+tbl+"]"); err != nil {
				t.Logf("cleanup drop %s: %v", tbl, err)
			}
		}
	}
	t.Cleanup(dropAll)

	// 独立连接做校验：写入路径与取证路径分开，才排掉"自己写的断言自己圆"。
	probe, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatalf("probe open: %v", err)
	}
	defer probe.Close()

	totalCols := 0
	for i, f := range doc.Formats {
		tbl := tableNames[i]
		cfg := storage.TableConfig{
			Driver: "sqlserver", Dialect: "sqlserver", DSN: dsn,
			Table: tbl, FileTable: registry, FileKey: sink.FileKey,
			Columns: f.Columns,
		}
		s, err := storage.OpenTable(ctx, cfg)
		if err != nil {
			t.Fatalf("format %q OpenTable(%s): %v", f.Name, tbl, err)
		}
		var d model.CollectionDate
		if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
			t.Fatal(err)
		}
		header := make([]string, 0, len(f.Columns))
		for _, c := range f.Columns {
			header = append(header, c.Name)
		}
		// 值取现场量级：短字段，不含分隔符与控制字符。
		row := make([]string, 0, len(f.Columns))
		for j := range f.Columns {
			row = append(row, fmt.Sprintf("v%02d-样本", j+1))
		}
		fileName := strings.ReplaceAll(f.Match, "YYMMDD", "260909")
		batch := model.Batch{
			Key:       model.CollectionKey{SourceID: model.SourceID("50#-" + f.Name), Date: d},
			File:      model.FileIdentity{SourceID: model.SourceID("50#-" + f.Name), Path: `\\192.168.1.150\log\202609\` + fileName, Name: fileName, Hash: "cfg-" + f.Name},
			Header:    header,
			Sequence:  1,
			CreatedAt: time.Date(2026, 9, 10, 9, 1, 0, 0, time.UTC),
			Records: []model.Record{
				{RowNumber: 1, Fields: row},
				{RowNumber: 2, Fields: row},
			},
		}
		if err := s.Write(ctx, batch); err != nil {
			t.Fatalf("format %q（%d 列）write: %v", f.Name, len(f.Columns), err)
		}
		// 幂等重放：同文件再写一次不该多行。
		if err := s.Write(ctx, batch); err != nil {
			t.Fatalf("format %q replay: %v", f.Name, err)
		}
		var rows int
		if err := probe.QueryRowContext(ctx, "SELECT COUNT(*) FROM ["+tbl+"]").Scan(&rows); err != nil {
			t.Fatalf("format %q count: %v", f.Name, err)
		}
		if rows != 2 {
			t.Errorf("format %q rows = %d, want 2（重放应幂等）", f.Name, rows)
		}
		var cols int
		if err := probe.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID(@p1)", tbl).Scan(&cols); err != nil {
			t.Fatalf("format %q sys.columns: %v", f.Name, err)
		}
		// file_key 骨架：数据表 = 键 2 列 + 声明列，不含 file_id。
		want := len(f.Columns) + 2
		if cols != want {
			t.Errorf("format %q columns = %d, want %d (file_key,row_number + 声明)", f.Name, cols, want)
		}
		totalCols += len(f.Columns)
		s.Close()
	}

	// 注册表与发号：23 个文件各一行、各一个互不相同的键。
	var regRows, keys int
	if err := probe.QueryRowContext(ctx, "SELECT COUNT(*) FROM ["+registry+"]").Scan(&regRows); err != nil {
		t.Fatalf("registry count: %v", err)
	}
	if regRows != len(doc.Formats) {
		t.Errorf("registry rows = %d, want %d", regRows, len(doc.Formats))
	}
	if err := probe.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT file_key) FROM ["+registry+"]").Scan(&keys); err != nil {
		t.Fatalf("registry distinct keys: %v", err)
	}
	if keys != len(doc.Formats) {
		t.Errorf("distinct file_key = %d, want %d（同一条计数器发过 23 个键）", keys, len(doc.Formats))
	}
	// 表头随文件落库（注册表的 header 列是 NVARCHAR(4000)，最长的 70 列表头
	// 要装得下，否则现场排错就没有原始表头可对照）。
	var longest int
	if err := probe.QueryRowContext(ctx,
		"SELECT MAX(LEN(header)) FROM ["+registry+"]").Scan(&longest); err != nil {
		t.Fatalf("registry header length: %v", err)
	}
	t.Logf("落库：%d 张表 / 共 %d 个声明列；注册表 %d 行、%d 个键，最长表头 %d 字符",
		len(doc.Formats), totalCols, regRows, keys, longest)
}
