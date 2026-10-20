package configutil

import (
	"fmt"
	"strconv"

	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/errs"
)

func OptionalString(cc config.ComponentConfig, key, def string) string {
	if v, ok := cc.Config[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return def
}

func RequiredString(cc config.ComponentConfig, key string) (string, error) {
	if v, ok := cc.Config[key]; ok && v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("%w: component %q missing string config %q", errs.ErrInvalidConfig, cc.ID, key)
}

func OptionalInt(cc config.ComponentConfig, key string, def int) int {
	v, ok := cc.Config[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

// OptionalIntPtr 返回可选整型的指针：键缺省为 nil。用于"未设置"与
// "显式设为 0"必须区分的开关（如 file_key_node，0 是合法节点号）。
func OptionalIntPtr(cc config.ComponentConfig, key string) *int {
	if _, ok := cc.Config[key]; !ok {
		return nil
	}
	n := OptionalInt(cc, key, 0)
	return &n
}

func OptionalBool(cc config.ComponentConfig, key string, def bool) bool {
	v, ok := cc.Config[key]
	if !ok {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true"
	}
	return def
}
