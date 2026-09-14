package host

import (
	"regexp"

	"dynamic-runtime/extensions/config"
)

// RedactedSentinel replaces sensitive values in every DISPLAY copy of a
// configuration (component config GET, effective-config, explorer). When a
// display copy is written back through SetComponentConfig, values equal to
// the sentinel are restored from the stored configuration instead of being
// persisted — round-tripping an untouched secret never destroys it.
const RedactedSentinel = "__REDACTED__"

var sensitiveKeyPattern = regexp.MustCompile(`(?i)(dsn|password|passwd|secret|token|credential|connection_string)`)

// redactConfigForDisplay deep-copies cfg and replaces the values of
// sensitive keys (dsn/password/secret/token/...) with the sentinel.
func redactConfigForDisplay(cfg config.Config) config.Config {
	out := config.Config{Components: make([]config.ComponentConfig, len(cfg.Components))}
	for i, cc := range cfg.Components {
		out.Components[i] = cc
		if cc.Config != nil {
			out.Components[i].Config = redactMap(cc.Config)
		}
	}
	return out
}

func redactMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if sensitiveKeyPattern.MatchString(k) {
			out[k] = RedactedSentinel
			continue
		}
		switch tv := v.(type) {
		case map[string]any:
			out[k] = redactMap(tv)
		case []any:
			out[k] = redactSlice(tv)
		default:
			out[k] = v
		}
	}
	return out
}

func redactSlice(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		switch tv := v.(type) {
		case map[string]any:
			out[i] = redactMap(tv)
		case []any:
			out[i] = redactSlice(tv)
		default:
			out[i] = v
		}
	}
	return out
}

// restoreRedacted overwrites sentinel values in incoming with the stored
// values, so a display round-trip never persists "__REDACTED__".
func restoreRedacted(incoming, stored map[string]any) {
	for k, v := range incoming {
		sv, haveStored := stored[k]
		switch tv := v.(type) {
		case map[string]any:
			sm, _ := sv.(map[string]any)
			if sm == nil {
				sm = map[string]any{}
			}
			restoreRedacted(tv, sm)
			incoming[k] = tv
		case []any:
			sl, _ := sv.([]any)
			if sl != nil {
				restoreRedactedSlice(tv, sl)
			}
			incoming[k] = tv
		default:
			if s, ok := v.(string); ok && s == RedactedSentinel && haveStored {
				incoming[k] = sv
			}
		}
	}
}

func restoreRedactedSlice(incoming, stored []any) {
	for i := range incoming {
		if i >= len(stored) {
			return
		}
		switch tv := incoming[i].(type) {
		case map[string]any:
			sm, _ := stored[i].(map[string]any)
			if sm == nil {
				sm = map[string]any{}
			}
			restoreRedacted(tv, sm)
			incoming[i] = tv
		case []any:
			sl, _ := stored[i].([]any)
			if sl != nil {
				restoreRedactedSlice(tv, sl)
			}
			incoming[i] = tv
		default:
			if s, ok := incoming[i].(string); ok && s == RedactedSentinel && i < len(stored) {
				incoming[i] = stored[i]
			}
		}
	}
}
