package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	extconfig "dynamic-runtime/extensions/config"

	appmetadata "gocordis-csv-collector/app/metadata"
)

type TypeInfo struct {
	Kind       string
	Capability string
}

var knownTypes = map[string]TypeInfo{
	"local-file-source":  {Kind: "source", Capability: "filesource"},
	"unc-file-source":    {Kind: "source", Capability: "filesource"},
	"csv-parser":         {Kind: "parser", Capability: "csvparser"},
	"memory-storage":     {Kind: "storage", Capability: "storage"},
	"mysql-storage":      {Kind: "storage", Capability: "storage"},
	"postgresql-storage": {Kind: "storage", Capability: "storage"},
	"oracle-storage":     {Kind: "storage", Capability: "storage"},
	"memory-state":       {Kind: "state", Capability: "state"},
	"file-state":         {Kind: "state", Capability: "state"},
	"scheduler":          {Kind: "scheduler", Capability: "trigger"},
	"csv-collector":      {Kind: "collector", Capability: "collector"},
	"path-metadata":      {Kind: "metadata", Capability: "metadataextractor"},
}

// Validate rejects invalid application configuration before Runtime mutation.
func Validate(cfg extconfig.Config) error {
	byID := make(map[string]extconfig.ComponentConfig)
	for _, cc := range cfg.Components {
		if cc.ID == "" || cc.Type == "" {
			return fmt.Errorf("component missing id/type")
		}
		if _, dup := byID[cc.ID]; dup {
			return fmt.Errorf("duplicate component id %q", cc.ID)
		}
		byID[cc.ID] = cc
		ti, ok := knownTypes[cc.Type]
		if !ok {
			return fmt.Errorf("unknown component type %q", cc.Type)
		}
		if err := validateOne(cc, ti); err != nil {
			return err
		}
	}
	metadataCount := 0
	for id, cc := range byID {
		if cc.Type != "path-metadata" {
			continue
		}
		metadataCount++
		ref := str(cc.Config, "source")
		target, ok := byID[ref]
		if !ok {
			return fmt.Errorf("metadata component %q references missing source component %q", id, ref)
		}
		if knownTypes[target.Type].Kind != "source" {
			return fmt.Errorf("metadata component %q must reference a source component", id)
		}
	}
	collectorCount := 0
	for id, cc := range byID {
		if cc.Type != "csv-collector" {
			continue
		}
		collectorCount++
		for _, field := range []string{"source", "parser", "storage", "state"} {
			ref := str(cc.Config, field)
			target, ok := byID[ref]
			if !ok {
				return fmt.Errorf("collector %q references missing component %q", id, ref)
			}
			want := map[string]string{
				"source":  "source",
				"parser":  "parser",
				"storage": "storage",
				"state":   "state",
			}[field]
			if knownTypes[target.Type].Kind != want {
				return fmt.Errorf("collector %q field %s must reference a %s component", id, field, want)
			}
		}
	}
	if collectorCount > 1 {
		return errors.New("v0.1 supports one csv-collector per Runtime realm")
	}
	if metadataCount > 1 {
		return errors.New("v0.1 supports one path-metadata component per Runtime realm (single MetadataExtractor)")
	}
	if collectorCount == 1 && metadataCount == 0 {
		return errors.New("csv-collector requires one path-metadata component (metadata dependency)")
	}
	return nil
}

func validateOne(cc extconfig.ComponentConfig, ti TypeInfo) error {
	switch ti.Kind {
	case "source":
		if str(cc.Config, "root") == "" {
			return fmt.Errorf("source %q missing root", cc.ID)
		}
		if raw, ok := cc.Config["file_stable_window_seconds"]; ok {
			w, valid := intCfgValue(raw)
			if !valid {
				return fmt.Errorf("source %q file_stable_window_seconds must be an integer", cc.ID)
			}
			if w < 0 {
				return fmt.Errorf("source %q file_stable_window_seconds must be >= 0", cc.ID)
			}
		}
	case "parser":
		if raw, ok := cc.Config["skip_lines"]; ok {
			n, valid := intCfgValue(raw)
			if !valid {
				return fmt.Errorf("parser %q skip_lines must be an integer", cc.ID)
			}
			if n < 0 {
				return fmt.Errorf("parser %q skip_lines must be >= 0", cc.ID)
			}
		}
	case "storage":
		switch cc.Type {
		case "memory-storage":
		default:
			dsn := str(cc.Config, "dsn")
			if dsn == "" {
				return fmt.Errorf("storage %q missing dsn", cc.ID)
			}
			table := str(cc.Config, "table")
			if table == "" {
				table = "gocordis_records"
			}
			if !tableNameRE.MatchString(table) {
				return fmt.Errorf("storage %q table is not a simple identifier", cc.ID)
			}
		}
	case "state":
		if cc.Type == "file-state" && str(cc.Config, "path") == "" {
			return fmt.Errorf("file-state %q missing path", cc.ID)
		}
	case "scheduler":
		if str(cc.Config, "schedule") != "daily" {
			return fmt.Errorf("scheduler %q must use schedule=\"daily\"", cc.ID)
		}
		if _, err := parseClock(str(cc.Config, "time")); err != nil {
			return fmt.Errorf("scheduler %q: %w", cc.ID, err)
		}
	case "metadata":
		if str(cc.Config, "source") == "" {
			return fmt.Errorf("metadata component %q missing source", cc.ID)
		}
		if _, err := appmetadata.ParseRules(cc.Config["metadata"]); err != nil {
			return fmt.Errorf("metadata component %q: %w", cc.ID, err)
		}
	case "collector":
		for _, field := range []string{"source", "parser", "storage", "state"} {
			if str(cc.Config, field) == "" {
				return fmt.Errorf("collector %q missing %s", cc.ID, field)
			}
		}
		policy := str(cc.Config, "date_policy")
		if policy == "" {
			policy = "yesterday"
		}
		if policy != "yesterday" && policy != "specific" {
			return fmt.Errorf("collector %q invalid date_policy %q", cc.ID, policy)
		}
		if policy == "specific" {
			d := str(cc.Config, "specific_date")
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return fmt.Errorf("collector %q specific_date invalid", cc.ID)
			}
		}
		if raw, ok := cc.Config["batch_size"]; ok {
			b, valid := intCfgValue(raw)
			if !valid {
				return fmt.Errorf("collector %q batch_size must be an integer", cc.ID)
			}
			if b <= 0 {
				return fmt.Errorf("collector %q batch_size must be positive", cc.ID)
			}
		}
	}
	_ = ti.Capability
	return nil
}

var tableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func str(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func intCfgValue(raw any) (int, bool) {
	s := fmt.Sprint(raw)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func parseClock(s string) (time.Time, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return time.Time{}, errors.New("time must be HH:MM")
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return time.Time{}, errors.New("invalid hour")
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return time.Time{}, errors.New("invalid minute")
	}
	return time.Date(2000, 1, 1, h, m, 0, 0, time.UTC), nil
}

func AllowedSourceType(typ string) bool {
	return typ == "local-file-source" || typ == "unc-file-source"
}
