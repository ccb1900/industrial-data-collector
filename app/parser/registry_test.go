package parser

import (
	"testing"

	"gocordis-csv-collector/app/model"
)

// TestParserSchemaTravelsWithFactory：Schema 与工厂同一次注册——分开放就是
// F 迭代修过的"两份清单必然漂移"。UI 的动态表单、文档的键清单、校验的
// 未知键拒绝，全部从这里取。
func TestParserSchemaTravelsWithFactory(t *testing.T) {
	sc, ok := ParserSchemaFor("csv")
	if !ok {
		t.Fatal("csv schema missing")
	}
	if sc.Kind != "csv" || sc.Title == "" || len(sc.Fields) == 0 {
		t.Fatalf("csv schema = %+v", sc)
	}
	var names []string
	for _, f := range sc.Fields {
		names = append(names, f.Name)
		if f.Label == "" || f.Type == "" {
			t.Errorf("field %q missing label/type: %+v", f.Name, f)
		}
	}
	for _, want := range []string{"encoding", "header", "skip_lines", "delimiter"} {
		if !contains(names, want) {
			t.Errorf("csv schema missing field %q", want)
		}
	}
	if _, ok := ParserSchemaFor("csv-parser"); !ok {
		t.Error("alias must carry the same schema")
	}
	if _, ok := ParserSchemaFor("no-such"); ok {
		t.Error("unknown kind must have no schema")
	}
	// 无 Schema 的注册必须被拒绝：Schema 是 UI/文档/校验的契约，不是可选件。
	if err := RegisterParser("schema-less", func(map[string]any) (model.CSVParser, error) {
		return nil, nil
	}, ConfigSchema{}); err == nil {
		t.Fatal("registration without schema must be rejected")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
