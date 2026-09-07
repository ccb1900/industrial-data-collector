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
	cfg.Components[5].Config["storage"] = "missing-store"
	if err := Validate(cfg); err == nil {
		t.Fatal("missing component reference must be rejected")
	}
}

func TestValidateRejectsWrongReferenceKind(t *testing.T) {
	cfg := validConfig()
	cfg.Components[5].Config["storage"] = "src"
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
	cfg.Components[5].Config["batch_size"] = 0
	if err := Validate(cfg); err == nil {
		t.Fatal("zero batch size must be rejected")
	}
	cfg = validConfig()
	cfg.Components[5].Config["batch_size"] = "not-a-number"
	if err := Validate(cfg); err == nil {
		t.Fatal("non-numeric batch size must be rejected")
	}
}

func TestValidateAcceptsDefaultedAndRejectsInvalidNumericValues(t *testing.T) {
	cfg := validConfig()
	delete(cfg.Components[5].Config, "batch_size")
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
