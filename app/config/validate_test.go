package config

import (
	"testing"

	extconfig "dynamic-runtime/extensions/config"
)

func component(id, typ string, cfg map[string]any) extconfig.ComponentConfig {
	return extconfig.ComponentConfig{ID: id, Type: typ, Config: cfg}
}

func validConfig() extconfig.Config {
	return extconfig.Config{Components: []extconfig.ComponentConfig{
		component("src", "local-file-source", map[string]any{"root": "/data", "file_stable_window_seconds": 0}),
		component("parser", "csv-parser", map[string]any{"header": true}),
		component("store", "memory-storage", map[string]any{}),
		component("state", "memory-state", map[string]any{}),
		component("sched", "scheduler", map[string]any{"schedule": "daily", "time": "02:00"}),
		component("meta", "path-metadata", map[string]any{"source": "src"}),
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
	cfg.Components[5].Config["metadata"] = []any{
		map[string]any{"name": "line", "from": "path", "pattern": "{line}/{station}/*.csv", "required": true},
		map[string]any{"name": "station", "from": "path", "pattern": "{line}/{station}/*.csv"},
		map[string]any{"name": "product", "from": "filename", "pattern": "{product}.csv"},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid metadata rules rejected: %v", err)
	}
}

func TestValidateRejectsMetadataConfigErrors(t *testing.T) {
	ruleCfg := func(rules ...any) map[string]any {
		return map[string]any{"source": "src", "metadata": rules}
	}
	withRule := func(r map[string]any) map[string]any {
		return ruleCfg(map[string]any{
			"name": "line", "from": "path", "pattern": "{line}/{station}/*.csv", "required": true,
		}, r)
	}
	cases := []struct {
		name   string
		mutate func(extconfig.Config) extconfig.Config
	}{
		{
			"duplicate key",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = ruleCfg(
					map[string]any{"name": "line", "from": "path", "pattern": "{line}/*.csv"},
					map[string]any{"name": "line", "from": "filename", "pattern": "{line}.csv"},
				)
				return c
			},
		},
		{
			"bad from value",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = ruleCfg(map[string]any{"name": "line", "from": "content", "pattern": "{line}/*.csv"})
				return c
			},
		},
		{
			"rule key missing from pattern",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = ruleCfg(map[string]any{"name": "station", "from": "path", "pattern": "{line}/*.csv"})
				return c
			},
		},
		{
			"missing source reference",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = map[string]any{}
				return c
			},
		},
		{
			"source reference is not a source",
			func(c extconfig.Config) extconfig.Config {
				c.Components[5].Config = map[string]any{"source": "store"}
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
			_ = withRule // keep helper referenced for clarity
			if err := Validate(cfg); err == nil {
				t.Fatal("invalid metadata configuration must be rejected")
			}
		})
	}
}

func TestValidateRejectsMoreThanOneMetadataComponent(t *testing.T) {
	cfg := validConfig()
	cfg.Components = append(cfg.Components,
		component("meta2", "path-metadata", map[string]any{"source": "src"}))
	if err := Validate(cfg); err == nil {
		t.Fatal("multiple path-metadata components must be rejected in v0.1")
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
