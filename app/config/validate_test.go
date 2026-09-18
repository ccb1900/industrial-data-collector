package config

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"database/sql/driver"

	extconfig "dynamic-runtime/extensions/config"

	"gocordis-csv-collector/internal/pluginmeta"
)

// Validation reads the aggregated type table from the component packages'
// embedded manifests; a unit test of this package links none of them, so it
// registers the metadata its fixtures need.
func init() {
	pluginmeta.MustRegister([]byte(`
name = "validate-test"
title = "Validate Test"

[[types]]
name = "local-file-source"
kind = "source"
capability = "filesource"
title = "Local File Source"

[[types]]
name = "csv-parser"
kind = "parser"
capability = "csvparser"
title = "CSV Parser"

[[types]]
name = "memory-storage"
kind = "storage"
capability = "storage"
title = "Memory Storage"

[[types]]
name = "memory-state"
kind = "state"
capability = "state"
title = "Memory State"

[[types]]
name = "scheduler"
kind = "scheduler"
capability = "trigger"
title = "Scheduler"

[[types]]
name = "path-metadata"
kind = "metadata"
capability = "metadataextractor"
title = "Path Metadata"

[[types]]
name = "csv-collector"
kind = "collector"
capability = "collector"
title = "CSV Collector"

[[types]]
name = "query-provider"
kind = "query"
capability = "query"
title = "Query Provider"

[[types]]
name = "console-bridge"
kind = "console-bridge"
capability = "console-bridge"
title = "Console Bridge"

[[types]]
name = "console-rows"
kind = "console-bridge"
capability = "console-rows"
title = "Console Rows"

[[types]]
name = "ui"
kind = "ui-host"
capability = "ui"
title = "UI Host"

[[types]]
name = "ui-page"
kind = "ui-contribution"
capability = "ui-page"
title = "UI Page Contribution"

[[types]]
name = "ui-panel"
kind = "ui-contribution"
capability = "ui-panel"
title = "UI Panel Contribution"

[[types]]
name = "plugin-explorer"
kind = "ui-console-plugin"
capability = "plugin-explorer"
title = "Plugin Explorer"

[[types]]
name = "csv-source-unit"
kind = "source-unit"
capability = "source-unit"
title = "CSV Source Unit"

[[types]]
name = "file-state"
kind = "state"
capability = "state"
title = "File State"

[[types]]
name = "text-parser"
kind = "parser"
capability = "csvparser"
title = "Text Parser"
`), "validate_test")
}

func component(id, typ string, cfg map[string]any) extconfig.ComponentConfig {
	return extconfig.ComponentConfig{ID: id, Type: typ, Config: cfg}
}

func rule(name, from, pattern string, required bool) map[string]any {
	return map[string]any{"name": name, "from": from, "pattern": pattern, "required": required}
}

func metadataEntry(source, root string, rules ...any) map[string]any {
	m := map[string]any{"source": source, "root": root}
	if len(rules) > 0 {
		m["metadata"] = rules
	}
	return m
}

func metadataConfig(entries ...map[string]any) map[string]any {
	arr := make([]any, 0, len(entries))
	for _, e := range entries {
		arr = append(arr, e)
	}
	return map[string]any{"sources": arr}
}

func validConfig() extconfig.Config {
	return extconfig.Config{Components: []extconfig.ComponentConfig{
		component("src", "local-file-source", map[string]any{"root": "/data", "file_stable_window_seconds": 0}),
		component("parser", "csv-parser", map[string]any{"header": true}),
		component("store", "memory-storage", map[string]any{}),
		component("state", "memory-state", map[string]any{}),
		component("sched", "scheduler", map[string]any{"schedule": "daily", "time": "02:00"}),
		component("meta", "path-metadata", metadataConfig(metadataEntry("src", "/data"))),
		component("col", "csv-collector", map[string]any{
			"source": "src", "parser": "parser", "storage": "store", "state": "state",
			"date_policy": "yesterday", "batch_size": 100,
		}),
	}}
}

func TestValidateAcceptCompleteConfig(t *testing.T) {
	if err := Validate(validConfig()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsMissingReference(t *testing.T) {
	cfg := validConfig()
	cfg.Components[6].Config["storage"] = "missing-store"
	if err := Validate(cfg); err == nil {
		t.Fatal("missing component reference must be rejected")
	}
}

func TestValidateRejectsWrongReferenceKind(t *testing.T) {
	cfg := validConfig()
	cfg.Components[6].Config["storage"] = "src"
	if err := Validate(cfg); err == nil {
		t.Fatal("wrong reference kind must be rejected")
	}
}

func TestValidateRejectsBadScheduleAndBatch(t *testing.T) {
	cfg := validConfig()
	cfg.Components[4].Config["schedule"] = "weekly"
	if err := Validate(cfg); err == nil {
		t.Fatal("invalid schedule must be rejected")
	}
	cfg = validConfig()
	cfg.Components[6].Config["batch_size"] = 0
	if err := Validate(cfg); err == nil {
		t.Fatal("zero batch size must be rejected")
	}
	cfg = validConfig()
	cfg.Components[6].Config["batch_size"] = "not-a-number"
	if err := Validate(cfg); err == nil {
		t.Fatal("non-numeric batch size must be rejected")
	}
}

func TestValidateAcceptsDefaultedAndRejectsInvalidNumericValues(t *testing.T) {
	cfg := validConfig()
	delete(cfg.Components[6].Config, "batch_size")
	if err := Validate(cfg); err != nil {
		t.Fatalf("absent batch_size should use the factory default: %v", err)
	}
	cfg = validConfig()
	cfg.Components[0].Config["file_stable_window_seconds"] = "soon"
	if err := Validate(cfg); err == nil {
		t.Fatal("non-numeric stable window must be rejected")
	}
	cfg = validConfig()
	cfg.Components[0].Config["file_stable_window_seconds"] = -1
	if err := Validate(cfg); err == nil {
		t.Fatal("negative stable window must be rejected")
	}
}

func TestValidateAcceptsMetadataRules(t *testing.T) {
	cfg := validConfig()
	cfg.Components[5].Config = metadataConfig(metadataEntry("src", "/data",
		rule("line", "path", "{line}/{station}/*.csv", true),
		rule("station", "path", "{line}/{station}/*.csv", true),
		rule("product", "filename", "{product}.csv", true),
	))
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid metadata rules rejected: %v", err)
	}
}

func TestValidateAcceptsMultipleSourcesWithOwnRules(t *testing.T) {
	cfg := extconfig.Config{Components: []extconfig.ComponentConfig{
		component("src-a", "local-file-source", map[string]any{"root": "/data/a", "file_stable_window_seconds": 0}),
		component("src-b", "local-file-source", map[string]any{"root": "/data/b", "file_stable_window_seconds": 0}),
		component("parser", "csv-parser", map[string]any{"header": true}),
		component("store", "memory-storage", map[string]any{}),
		component("state", "memory-state", map[string]any{}),
		component("sched", "scheduler", map[string]any{"schedule": "daily", "time": "02:00"}),
		component("meta", "path-metadata", metadataConfig(
			metadataEntry("src-a", "/data/a",
				rule("line", "path", "{line}/{station}/{date}/*.csv", true),
				rule("station", "path", "{line}/{station}/{date}/*.csv", true),
			),
			metadataEntry("src-b", "/data/b",
				rule("product", "path", "{product}/{batch}/{date}.csv", true),
			),
		)),
		component("col", "csv-collector", map[string]any{
			"source": "src-a", "parser": "parser", "storage": "store", "state": "state",
			"date_policy": "yesterday", "batch_size": 100,
		}),
	}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("two sources with independent metadata rule sets must be accepted: %v", err)
	}
}

func TestValidateRejectsMetadataConfigErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(extconfig.Config) extconfig.Config
	}{
		{
			"duplicate key within one source",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = metadataConfig(metadataEntry("src", "/data",
					rule("line", "path", "{line}/*.csv", true),
					rule("line", "filename", "{line}.csv", true),
				))
				return c
			},
		},
		{
			"bad from value",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = metadataConfig(metadataEntry("src", "/data",
					rule("line", "content", "{line}/*.csv", true)))
				return c
			},
		},
		{
			"rule key missing from pattern",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = metadataConfig(metadataEntry("src", "/data",
					rule("station", "path", "{line}/*.csv", true)))
				return c
			},
		},
		{
			"entry missing source reference",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = metadataConfig(map[string]any{"root": "/data"})
				return c
			},
		},
		{
			"source reference is not a source",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = metadataConfig(metadataEntry("store", "/data"))
				return c
			},
		},
		{
			"source root mismatch",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = metadataConfig(metadataEntry("src", "/other"))
				return c
			},
		},
		{
			"duplicate source entry",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = metadataConfig(
					metadataEntry("src", "/data"),
					metadataEntry("src", "/data"),
				)
				return c
			},
		},
		{
			"metadata component missing when collector present",
			func(c extconfig.Config) extconfig.Config {
				comps := c.Components
				c.Components = append(comps[:5], comps[6:]...)
				return c
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.mutate(validConfig())
			if err := Validate(cfg); err == nil {
				t.Fatal("invalid metadata configuration must be rejected")
			}
		})
	}
}

// TestValidateRejectsOneSourceLeavesOtherValid verifies M-MULTI-03 at config
// level: an invalid rule set for one source is rejected, while the previous
// valid multi-source configuration remains accepted (old config stays valid).
func TestValidateRejectsOneSourceLeavesOtherValid(t *testing.T) {
	cfg := validConfig()
	cfg.Components[5].Config = metadataConfig(
		metadataEntry("src", "/data",
			rule("line", "path", "{line}/*.csv", true),
			rule("line", "filename", "{line}.csv", true),
		),
		metadataEntry("other", "/data",
			rule("product", "filename", "{product}.csv", true),
		),
	)
	// The invalid source makes the whole desired config invalid...
	if err := Validate(cfg); err == nil {
		t.Fatal("invalid rule set for one source must be rejected")
	}
	// ...and the previous valid configuration is unaffected.
	if err := Validate(validConfig()); err != nil {
		t.Fatalf("previous valid config must stay valid: %v", err)
	}
}

func TestValidateRejectsMoreThanOneMetadataComponent(t *testing.T) {
	cfg := validConfig()
	cfg.Components = append(cfg.Components,
		component("meta2", "path-metadata", metadataConfig(metadataEntry("src", "/data"))))
	if err := Validate(cfg); err == nil {
		t.Fatal("multiple path-metadata components must be rejected (one provider per Realm)")
	}
}

func TestValidateParserSkipLines(t *testing.T) {
	cfg := validConfig()
	cfg.Components[1].Config["skip_lines"] = 3
	if err := Validate(cfg); err != nil {
		t.Fatalf("positive skip_lines must be accepted: %v", err)
	}
	cfg = validConfig()
	cfg.Components[1].Config["skip_lines"] = -1
	if err := Validate(cfg); err == nil {
		t.Fatal("negative skip_lines must be rejected")
	}
	cfg = validConfig()
	cfg.Components[1].Config["skip_lines"] = "many"
	if err := Validate(cfg); err == nil {
		t.Fatal("non-numeric skip_lines must be rejected")
	}
}

func TestValidateParserStructuredMetadataConfig(t *testing.T) {
	structured := func(start, end, header int, extra func(map[string]any)) extconfig.Config {
		cfg := validConfig()
		parserCfg := map[string]any{
			"header": true,
			"csv": map[string]any{
				"metadata": map[string]any{"mode": "key_value", "start_row": start, "end_row": end},
				"data":     map[string]any{"header_row": header},
			},
		}
		if extra != nil {
			extra(parserCfg)
		}
		cfg.Components[1].Config = parserCfg
		return cfg
	}
	if err := Validate(structured(1, 2, 4, nil)); err != nil {
		t.Fatalf("valid structured parser config rejected: %v", err)
	}
	bad := []func() extconfig.Config{
		func() extconfig.Config { return structured(0, 2, 4, nil) },
		func() extconfig.Config { return structured(5, 2, 4, nil) },
		func() extconfig.Config { return structured(1, 2, 2, nil) },
		func() extconfig.Config {
			return structured(1, 2, 4, func(m map[string]any) { m["header"] = false })
		},
		func() extconfig.Config {
			return structured(1, 2, 4, func(m map[string]any) { m["skip_lines"] = 1 })
		},
	}
	for i, makeCfg := range bad {
		if err := Validate(makeCfg()); err == nil {
			t.Fatalf("invalid structured parser case %d must be rejected", i)
		}
	}
}

func TestValidateAcceptsQueryAndUIComponents(t *testing.T) {
	cfg := validConfig()
	cfg.Components = append(cfg.Components,
		component("query-provider", "query-provider", map[string]any{}),
		component("ui", "ui", map[string]any{}),
	)
	if err := Validate(cfg); err != nil {
		t.Fatalf("query-provider/ui components must be accepted: %v", err)
	}
}

// 同一 (dsn, table) 的列集不一致必然是配错——拒绝启动而不是静默把两种
// 业务写进一张表。同表同列（靠 source_id 区分）保持合法。
func TestValidateRejectsSameTableDifferentColumns(t *testing.T) {
	registerSQLiteDriverOnce()
	base := func(id, table string, col string) extconfig.ComponentConfig {
		return extconfig.ComponentConfig{
			ID: id, Type: "csv-source-unit",
			Config: map[string]any{
				"source_id": id, "path": "/tmp/x", "storage": "sqlite-storage",
				"driver": "sqlite", "dsn": "state/collect.db", "table": table,
				"columns": []any{
					map[string]any{"name": col, "column": col, "type": "text"},
				},
			},
		}
	}
	cfg := extconfig.Config{Components: []extconfig.ComponentConfig{
		base("a", "shared", "x"),
		base("b", "shared", "y"),
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "different column declarations") {
		t.Fatalf("err = %v, want different-column rejection", err)
	}

	// 同表同列：合法。
	cfg.Components[1].Config["columns"] = cfg.Components[0].Config["columns"]
	if err := Validate(cfg); err != nil {
		t.Fatalf("same columns must be legal, got %v", err)
	}
}

// 桩驱动：单元测试二进制不链接真实驱动包，驱动探针只查 sql.Drivers()
// 成员名，注册一个同名桩即可让组合校验走到交叉检查。
var registerSQLiteStub sync.Once

func registerSQLiteDriverOnce() {
	registerSQLiteStub.Do(func() {
		sql.Register("sqlite", stubDriver{})
	})
}

type stubDriver struct{}

func (stubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("stub") }
