package host

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"dynamic-runtime/extensions/config"
)

func TestRedactMapDeep(t *testing.T) {
	in := map[string]any{
		"dsn":      "postgres://u:p@host/db",
		"password": "secret",
		"plain":    "value",
		"nested": map[string]any{
			"token": "tk-1",
			"keep":  42,
		},
		"list": []any{
			map[string]any{"secret": "s1", "ok": true},
			"plain",
		},
	}
	out := redactMap(in)
	if out["dsn"] != RedactedSentinel || out["password"] != RedactedSentinel {
		t.Fatalf("top-level secrets not redacted: %v", out)
	}
	nested := out["nested"].(map[string]any)
	if nested["token"] != RedactedSentinel || nested["keep"] != 42 {
		t.Fatalf("nested redaction = %v", nested)
	}
	l0 := out["list"].([]any)[0].(map[string]any)
	if l0["secret"] != RedactedSentinel || l0["ok"] != true {
		t.Fatalf("slice redaction = %v", l0)
	}
	if in["dsn"] == RedactedSentinel {
		t.Fatal("input was mutated")
	}
	if _, has := out["plain"]; !has {
		t.Fatal("plain key lost")
	}
}

func TestRestoreRedactedScalarsAndArrays(t *testing.T) {
	stored := map[string]any{
		"dsn": "real-dsn",
		"list": []any{
			map[string]any{"secret": "real-s1"},
		},
	}
	incoming := map[string]any{
		"dsn": RedactedSentinel,
		"list": []any{
			map[string]any{"secret": RedactedSentinel},
		},
	}
	restoreRedacted(incoming, stored)
	if incoming["dsn"] != "real-dsn" {
		t.Fatalf("scalar restore = %v", incoming["dsn"])
	}
	elem := incoming["list"].([]any)[0].(map[string]any)
	if elem["secret"] != "real-s1" {
		t.Fatalf("array element restore = %v", elem)
	}
}

// 数组内哨兵按键名同位置回填（当前配置模型中数组不承载敏感键；
// 该测试钉住标量哨兵在数组对象内的回填行为）。
func TestRestoreRedactedArrayScalar(t *testing.T) {
	stored := []any{
		map[string]any{"name": "first", "dsn": "real-one"},
	}
	incoming := []any{
		map[string]any{"name": "first-edited", "dsn": RedactedSentinel},
	}
	restoreRedactedSlice(incoming, stored)
	elem := incoming[0].(map[string]any)
	if elem["dsn"] != "real-one" || elem["name"] != "first-edited" {
		t.Fatalf("positional restore = %v", elem)
	}
}

func TestSetComponentConfigRoundTripKeepsSecret(t *testing.T) {
	h, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	h.setBaseDesired(config.Config{Components: []config.ComponentConfig{
		{ID: "sched", Type: "scheduler", Config: map[string]any{
			"dsn":  "real-secret-dsn",
			"name": "n",
			"cron": "23 3 * * *",
		}},
	}})

	got, err := h.ComponentConfig("sched")
	if err != nil {
		t.Fatal(err)
	}
	if got["dsn"] != RedactedSentinel {
		t.Fatalf("display dsn = %v", got["dsn"])
	}

	edited := map[string]any{"dsn": RedactedSentinel, "name": "renamed", "cron": "23 3 * * *"}
	if err := h.SetComponentConfig(context.Background(), "sched", edited); err != nil {
		t.Fatal(err)
	}
	stored, ok := h.storedComponentConfig("sched")
	if !ok {
		t.Fatal("stored config missing")
	}
	if stored.Config["dsn"] != "real-secret-dsn" {
		t.Fatalf("round-trip destroyed the secret: %v", stored.Config)
	}
	if stored.Config["name"] != "renamed" {
		t.Fatalf("edit lost: %v", stored.Config)
	}
	// 展示出口依旧脱敏。
	if got, _ := h.ComponentConfig("sched"); got["dsn"] != RedactedSentinel {
		t.Fatalf("display must stay redacted: %v", got)
	}
}
