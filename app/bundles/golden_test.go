package bundles

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	extbundle "dynamic-runtime/extensions/bundle"
)

// goldenUpdate 让"有意改预设"变成一条命令，而不是让人手抄 12KB JSON 进
// testdata——那种活儿没人会认真做，最后就是 `git checkout --` 悄悄回滚掉守护。
var goldenUpdate = flag.Bool("update", false, "重写 testdata/presets.golden.json 后再比较")

// 数据化守护：Expand 输出必须与金样一致（ids/types/config 深度相等）。
// 金样由 Go 字面量时代生成，任何预设漂移都在此处暴露。
func TestPresetsMatchGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "presets.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	if *goldenUpdate {
		preset := map[string]any{}
		for _, name := range []string{"collector-core", "collector-console"} {
			rows, err := extbundle.Expand([]string{name})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			preset[name] = rows
		}
		data, err := json.MarshalIndent(preset, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "presets.golden.json"), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("金样已按当前预设重写")
		return
	}
	for _, name := range []string{"collector-core", "collector-console"} {
		rows, err := extbundle.Expand([]string{name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		data, merr := json.Marshal(rows)
		if merr != nil {
			t.Fatal(merr)
		}
		var got any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		wantRows := want[name].([]any)
		gotRows := got.([]any)
		if len(gotRows) != len(wantRows) {
			t.Fatalf("%s: %d rows, want %d", name, len(gotRows), len(wantRows))
		}
		gd, _ := json.Marshal(gotRows)
		wd, _ := json.Marshal(wantRows)
		if string(gd) != string(wd) {
			t.Fatalf("%s drifted:\n got %s\nwant %s", name, gd, wd)
		}
	}
}
