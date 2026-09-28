// fleet 声明的脱敏显示与写回还原：与组件配置的 RedactedSentinel 机制
// 同一约定——显示副本永不泄露凭证，round-trip 写回永不毁掉真实值。
package host

import (
	"encoding/json"
	"fmt"

	"dynamic-runtime/extensions/configwatch"

	"gocordis-csv-collector/app/fleetstore"
)

// unredactedFleet loads the current effective fleet document WITHOUT
// redaction (store when present, else the seed file) — the restore source.
func (h *Host) unredactedFleet() (fleetstore.FleetDoc, error) {
	h.mu.Lock()
	store, seed := h.fleet, h.fleetSeedData
	h.mu.Unlock()
	if store != nil {
		if doc, exists, err := store.Load(); err == nil && exists {
			return doc, nil
		}
	}
	if len(seed) == 0 {
		return fleetstore.FleetDoc{}, nil
	}
	doc, err := configwatch.ParseDocument(seed)
	if err != nil {
		return fleetstore.FleetDoc{}, err
	}
	return fleetstore.FromDocument(doc), nil
}

// redactFleet returns a display copy: sensitive-key values replaced by the
// sentinel (same key pattern as component configs).
func redactFleet(doc fleetstore.FleetDoc) fleetstore.FleetDoc {
	var out fleetstore.FleetDoc
	remarshal(doc, &out, true)
	return out
}

// restoreFleetSecrets overwrites sentinel values in incoming with the
// current effective values. sinks 按(name)、machines 按(no)身份匹配——
// 删除/重排行不会让密钥串位；其余结构按位置还原（数组当前不承载
// 敏感键，见 restoreRedactedSlice 的边界说明）。
func restoreFleetSecrets(incoming, stored *fleetstore.FleetDoc) error {
	inMap, stMap, err := fleetMaps(*incoming, *stored)
	if err != nil {
		return err
	}
	restoreRedacted(inMap, stMap)
	restoreByIdentity(inMap["machines"], stMap["machines"], "no")
	restoreByIdentity(inMap["sinks"], stMap["sinks"], "name")
	// 残留哨兵 = 新增行携带占位符（没有可还原的真实值）——落库会把它
	// 变成真实配置值，必须显式拒绝。
	if err := rejectLeftoverSentinels(inMap); err != nil {
		return err
	}
	return remarshal(inMap, incoming, false)
}

func rejectLeftoverSentinels(m map[string]any) error {
	var found string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch tv := v.(type) {
		case map[string]any:
			for k, item := range tv {
				walk(path+"."+k, item)
			}
		case []any:
			for i, item := range tv {
				walk(fmt.Sprintf("%s[%d]", path, i), item)
			}
		case string:
			if tv == RedactedSentinel && found == "" {
				found = path
			}
		}
	}
	walk("", m)
	if found != "" {
		return fmt.Errorf("new value at %s is the redaction placeholder — provide the real value", found)
	}
	return nil
}

// restoreByIdentity re-restores identity-keyed arrays: a row with the same
// id keeps ITS secret even after other rows were added or removed.
func restoreByIdentity(incoming, stored any, idKey string) {
	inRows, ok := incoming.([]any)
	if !ok {
		return
	}
	stRows, _ := stored.([]any)
	byID := map[string]map[string]any{}
	for _, raw := range stRows {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := m[idKey].(string); ok {
			byID[id] = m
		}
	}
	for _, raw := range inRows {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m[idKey].(string)
		storedRow := byID[id]
		if storedRow == nil {
			continue
		}
		restoreRedacted(m, storedRow)
	}
}

func fleetMaps(in, stored fleetstore.FleetDoc) (map[string]any, map[string]any, error) {
	var inM, stM map[string]any
	if err := remarshal(in, &inM, false); err != nil {
		return nil, nil, err
	}
	if err := remarshal(stored, &stM, false); err != nil {
		return nil, nil, err
	}
	return inM, stM, nil
}

// remarshal converts between FleetDoc and its map form. redact=true applies
// sensitive-key redaction on the map side.
func remarshal(in any, out any, redact bool) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("fleet remarshal: %w", err)
	}
	if redact {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("fleet remarshal: %w", err)
		}
		raw, err = json.Marshal(redactMap(m))
		if err != nil {
			return fmt.Errorf("fleet remarshal: %w", err)
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("fleet remarshal: %w", err)
	}
	return nil
}
