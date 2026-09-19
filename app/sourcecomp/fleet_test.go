package sourcecomp

import (
	"runtime"
	"strings"
	"testing"
)

// 四层展开：机台 × 其组的格式 → 源定义；defaults 下沉；机台元数据并表。
func TestExpandFleetBasic(t *testing.T) {
	d := FleetDefaults{StateDir: "../state", DatePolicy: "yesterday", CatchupDays: 31, BatchSize: 1000}
	sinks := []SinkDef{{Name: "db", Driver: "oracle", DSN: "oracle://x", FileTable: "files"}}
	formats := []FormatDef{
		{Name: "aaa", Match: "aaa_YYMMDD.log", Table: "plant_aaa", Sink: "db"},
		{Name: "mainte", Match: "mainte1_*.log", Table: "plant_mainte", Sink: "db"}, // 无日期记号
	}
	groups := []FormatGroupDef{{Name: "typeA-set", Formats: []string{"aaa", "mainte"}}}
	machines := []MachineDef{
		{No: "A-01", IP: "10.0.0.1", Path: `\\{ip}\logs\YYYYMM`, Group: "typeA-set", Since: "2025-03-01",
			Metadata: map[string]string{"line": "1"}},
		{No: "A-02", IP: "10.0.0.2", Path: `\\{ip}\logs\YYYYMM`, Group: "typeA-set"},
	}
	out, _, err := expandFleet(d, sinks, formats, groups, machines, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("expanded = %d, want 4", len(out))
	}
	first := out[0]
	if first.ID != "A-01-aaa" {
		t.Fatalf("id = %q", first.ID)
	}
	wantPath := "//10.0.0.1/logs"
	if runtime.GOOS == "windows" {
		wantPath = `\\10.0.0.1\logs`
	}
	if first.Path != wantPath {
		t.Fatalf("path = %q (静态根), want %q", first.Path, wantPath)
	}
	if first.Config["date_dir_layout"] != "YYYYMM" {
		t.Fatalf("date_dir_layout = %#v", first.Config["date_dir_layout"])
	}
	if first.Config["filename_date_layout"] != "aaa_YYMMDD.log" {
		t.Fatalf("filename layout = %#v", first.Config["filename_date_layout"])
	}
	if first.Config["pattern"] != "aaa_*.log" {
		t.Fatalf("pattern = %#v", first.Config["pattern"])
	}
	if first.Metadata["machine_no"] != "A-01" || first.Metadata["line"] != "1" {
		t.Fatalf("metadata = %#v", first.Metadata)
	}
	// 无日期记号的格式（低频维护日志）走目录发现，不携带文件名日期布局。
	// 找到 A-01-mainte 验证。
	var mainte *ResolvedSource
	for _, r := range out {
		if r.ID == "A-01-mainte" {
			mainte = r
		}
	}
	if mainte == nil || mainte.Config["filename_date_layout"] != nil || mainte.Config["pattern"] != "mainte1_*.log" {
		t.Fatalf("mainte 路由 = %#v", mainte.Config)
	}
}

// 引用完整性：缺格式/缺 sink/缺组/机台缺组 → 响亮失败。
func TestExpandFleetReferenceIntegrity(t *testing.T) {
	d := FleetDefaults{DatePolicy: "yesterday"}
	base := func() ([]SinkDef, []FormatDef, []FormatGroupDef, []MachineDef) {
		return []SinkDef{{Name: "db", Driver: "sqlite", DSN: ":memory:"}},
			[]FormatDef{{Name: "f", Match: "x_YYMMDD.csv", Table: "t", Sink: "db"}},
			[]FormatGroupDef{{Name: "g", Formats: []string{"f"}}},
			[]MachineDef{{No: "m1", Path: `\\h\d\YYYYMM`, Group: "g"}}
	}
	sinks, formats, groups, machines := base()
	formats[0].Sink = "nope"
	if _, _, err := expandFleet(d, sinks, formats, groups, machines, nil); err == nil || !strings.Contains(err.Error(), "unknown sink") {
		t.Fatalf("missing sink: %v", err)
	}
	sinks, formats, groups, machines = base()
	groups[0].Formats = []string{"ghost"}
	if _, _, err := expandFleet(d, sinks, formats, groups, machines, nil); err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Fatalf("missing format: %v", err)
	}
	sinks, formats, groups, machines = base()
	machines[0].Group = "ghost"
	if _, _, err := expandFleet(d, sinks, formats, groups, machines, nil); err == nil || !strings.Contains(err.Error(), "unknown format group") {
		t.Fatalf("missing group: %v", err)
	}
	// 日期归因：机台 path 与 match 都没有日期记号 → 拒绝。
	sinks, formats, groups, machines = base()
	formats[0].Match = "x.csv"
	machines[0].Path = `\\h\data`
	if _, _, err := expandFleet(d, sinks, formats, groups, machines, nil); err == nil || !strings.Contains(err.Error(), "date token") {
		t.Fatalf("no date attribution: %v", err)
	}
}

// Parse 集成：四层声明经组合管线展开为源行，defaults 与 sink 字段下沉。
func TestParseFourLayerFleet(t *testing.T) {
	doc := `
defaults = { state_dir = "../state", date_policy = "yesterday", catchup_days = 31, batch_size = 1000, expect = "daily", inspection_lookback_days = 2 }

[[sinks]]
name = "plant-db"
driver = "sqlite"
dsn = "../state/plant.db"
file_table = "plant_files"

[[formats]]
name = "aaa"
match = "aaa_YYMMDD.log"
table = "plant_aaa"
sink = "plant-db"

[[formats]]
name = "mainte"
match = "mainte1_*.log"
table = "plant_mainte"
sink = "plant-db"
expect = ""

[formats.parser]
encoding = "gb18030"
allow_ragged = true

[[format_groups]]
name = "typeA-set"
formats = ["aaa", "mainte"]

[[machines]]
no = "A-01"
ip = "192.168.1.100"
path = '\\{ip}\logs\YYYYMM'
group = "typeA-set"
since = "2025-03-01"

[machines.metadata]
line = "1"

[[components]]
id = "query-provider"
type = "query-provider"
`
	res, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(res.Sources))
	}
	var aaa, mainte *ResolvedSource
	for _, r := range res.Sources {
		switch r.ID {
		case "A-01-aaa":
			aaa = r
		case "A-01-mainte":
			mainte = r
		}
	}
	if aaa == nil || mainte == nil {
		t.Fatalf("expanded ids missing: %+v", res.Sources)
	}
	// 低频格式：expect 置空覆盖默认，不巡检（键缺席或空串皆可）。
	if v, ok := mainte.Config["expect"]; ok && v != "" {
		t.Fatalf("mainte expect = %#v, want empty", mainte.Config["expect"])
	}
	if mainte.Config["encoding"] != "gb18030" || mainte.Config["allow_ragged"] != true {
		t.Fatalf("mainte parser = %#v", mainte.Config)
	}
	if mainte.Config["since"] != "2025-03-01" {
		t.Fatalf("since = %#v", mainte.Config["since"])
	}
	if !strings.Contains(mainte.Path, "192.168.1.100") {
		t.Fatalf("path template not substituted: %q", mainte.Path)
	}
}
