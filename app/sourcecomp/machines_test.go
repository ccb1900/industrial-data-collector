package sourcecomp

import (
	"strings"
	"testing"
)

// 机台清单展开：一份 [[machines]] + 每格式一个 [[machine_formats]]，
// 生成 每机台 × 每格式 的源组件行；机台号/IP 注入静态元数据。
func TestMachineListExpansion(t *testing.T) {
	doc := `
[profiles.fmt]
parser = "csv"
header = true

[profiles.sink]
storage = "memory-storage"

[[machines]]
ip = "192.168.1.100"
no = "MT-001"

[[machines]]
ip = "192.168.1.101"
no = "MT-002"

[[machine_formats]]
path_template = '\\\\{ip}\\logs'
profiles = ["fmt", "sink"]
pattern = "a_*.log"
date_dir_layout = "200601"
filename_date_layout = "a_20060102.log"
id_suffix = "a"

[[machine_formats]]
path_template = '\\\\{ip}\\logs'
profiles = ["fmt", "sink"]
id_suffix = "b"

[[components]]
id = "scheduler"
type = "scheduler"
`
	res, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	comps := res.Config.Components
	var ids []string
	for _, c := range comps {
		if c.Type == "csv-source-unit" {
			ids = append(ids, c.ID)
		}
	}
	want := "source-unit:MT-001-a,source-unit:MT-001-b,source-unit:MT-002-a,source-unit:MT-002-b"
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	// 路径模板 {ip} 替换 + 覆盖键（pattern/date 布局）进入源配置。
	var row map[string]any
	for _, c := range comps {
		if c.ID == "source-unit:MT-001-a" {
			row = c.Config
		}
	}
	if row["pattern"] != "a_*.log" || row["date_dir_layout"] != "200601" {
		t.Fatalf("overrides missing: %v", row)
	}
	// 机台号/IP 注入静态元数据。
	mdAny := row["metadata"]
	md := map[string]any{}
	for k, v := range mdAny.(map[string]string) {
		md[k] = v
	}
	if md["machine_no"] != "MT-001" || md["ip"] != "192.168.1.100" {
		t.Fatalf("metadata = %v", md)
	}
	// 与显式 [[sources]] 冲突同样按 id 报错（配置错误必须可见）。
	if _, err := Parse([]byte(doc + `
[[sources]]
id = "MT-001-a"
path = "./x"
profiles = []
`)); err == nil || !strings.Contains(err.Error(), "duplicate source id") {
		t.Fatalf("duplicate id must error, got %v", err)
	}
}

// 单格式清单：无 id_suffix 时源 id = 机台号。
func TestMachineListSingleFormat(t *testing.T) {
	doc := `
[profiles.fmt]
parser = "csv"
header = true

[[machines]]
ip = "10.0.0.5"
no = "MT-009"

[[machine_formats]]
path_template = '\\\\{ip}\\logs'
profiles = ["fmt"]
id_suffix = "c"
`
	if _, err := Parse([]byte(doc)); err != nil {
		t.Fatal(err)
	}
}

func TestMachineListValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing ip", "[[machines]]\nno = \"x\"\n\n[[machine_formats]]\npath_template = '\\\\{ip}\\l'\nprofiles = [\"p\"]", "ip and no are required"},
		{"missing profiles", "[[machines]]\nip = \"1.2.3.4\"\nno = \"x\"\n\n[[machine_formats]]\npath_template = '\\\\{ip}\\l'", "profiles is required"},
		{"missing path_template", "[[machine_formats]]\nprofiles = [\"p\"]", "path_template is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body + "\n\n[[machine_formats]]\npath_template = '\\\\{ip}\\l'\nprofiles = [\"p\"]\n"
			_, err := Parse([]byte(body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
