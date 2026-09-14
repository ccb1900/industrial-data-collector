package host

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/internal/pluginmeta"
)

// R1 回归钉：bundle 预设的无配置组件（Config == nil）的配置 GET 必须返回
// 空 map，而不是 "not part of the desired configuration" 错误。
func TestComponentConfigNilConfigReturnsEmptyMap(t *testing.T) {
	pluginmeta.MustRegister([]byte(`
name = "host-test"
title = "Host Test"

[[types]]
name = "nil-config-type"
kind = "test"
capability = "test"
title = "Nil Config Type"
`), "host-test")

	h, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	h.setBaseDesired(config.Config{Components: []config.ComponentConfig{
		{ID: "nil-cfg-comp", Type: "nil-config-type"},
	}})
	got, err := h.ComponentConfig("nil-cfg-comp")
	if err != nil {
		t.Fatalf("nil-config component GET must succeed, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty map", got)
	}
}
