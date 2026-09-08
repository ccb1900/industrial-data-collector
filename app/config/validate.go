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
	Name       string
}

var knownTypes = map[string]TypeInfo{
	"local-file-source":  {Kind: "source", Capability: "filesource", Name: "Local File Source"},
	"unc-file-source":    {Kind: "source", Capability: "filesource", Name: "UNC File Source"},
	"csv-parser":         {Kind: "parser", Capability: "csvparser", Name: "CSV Parser"},
	"memory-storage":     {Kind: "storage", Capability: "storage", Name: "Memory Storage"},
	"mysql-storage":      {Kind: "storage", Capability: "storage", Name: "MySQL Storage"},
	"postgresql-storage": {Kind: "storage", Capability: "storage", Name: "PostgreSQL Storage"},
	"oracle-storage":     {Kind: "storage", Capability: "storage", Name: "Oracle Storage"},
	"memory-state":       {Kind: "state", Capability: "state", Name: "Memory State"},
	"file-state":         {Kind: "state", Capability: "state", Name: "File State"},
	"scheduler":          {Kind: "scheduler", Capability: "trigger", Name: "Scheduler"},
	"csv-collector":      {Kind: "collector", Capability: "collector", Name: "CSV Collector"},
	"path-metadata":      {Kind: "metadata", Capability: "metadataextractor", Name: "Path Metadata"},
	"query-provider":     {Kind: "query", Capability: "query", Name: "Query Provider"},
	"ui":                 {Kind: "ui-host", Capability: "ui", Name: "UI Host"},
	"ui-page":            {Kind: "ui-contribution", Capability: "ui-page", Name: "UI Page Contribution"},
	"ui-panel":           {Kind: "ui-contribution", Capability: "ui-panel", Name: "UI Panel Contribution"},
	"ui-contribution":    {Kind: "ui-contribution", Capability: "ui-contribution", Name: "UI Contribution"},
	"plugin-explorer":    {Kind: "ui-console-plugin", Capability: "plugin-explorer", Name: "Plugin Explorer"},
}

// DisplayName returns the human-facing plugin label for a known component
// type. Unknown/empty values fall back to the raw type.
func DisplayName(typ string) string {
	if ti, ok := knownTypes[typ]; ok && ti.Name != "" {
		return ti.Name
	}
	return typ
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
	// One path-metadata component = the single MetadataExtractor Provider of
	// the Realm. It may carry per-source rule sets for many sources; it never
	// becomes one provider per source.
	metadataCount := 0
	for id, cc := range byID {
		if cc.Type != "path-metadata" {
			continue
		}
		metadataCount++
		sets, err := appmetadata.ParseSourceConfig(cc.Config["sources"])
		if err != nil {
			return fmt.Errorf("metadata component %q: %w", id, err)
		}
		for _, set := range sets {
			ref := string(set.SourceID)
			target, ok := byID[ref]
			if !ok {
				return fmt.Errorf("metadata component %q references missing source component %q", id, ref)
			}
			if knownTypes[target.Type].Kind != "source" {
				return fmt.Errorf("metadata component %q source %q must reference a source component", id, ref)
			}
			if str(target.Config, "root") != set.Root {
				return fmt.Errorf("metadata component %q source %q root %q does not match source root %q", id, ref, set.Root, str(target.Config, "root"))
			}
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
		return errors.New("v0.1 supports exactly one path-metadata component per Runtime realm (single MetadataExtractor provider)")
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
		if _, err := appmetadata.ParseSourceConfig(cc.Config["sources"]); err != nil {
			return fmt.Errorf("metadata component %q: %w", cc.ID, err)
		}
	case "ui-contribution":
		switch cc.Type {
		case "ui-page":
			for _, field := range []string{"page_id", "title", "route", "renderer"} {
				if str(cc.Config, field) == "" {
					return fmt.Errorf("ui-page %q missing %s", cc.ID, field)
				}
			}
		case "ui-panel":
			for _, field := range []string{"panel_id", "title", "position", "renderer"} {
				if str(cc.Config, field) == "" {
					return fmt.Errorf("ui-panel %q missing %s", cc.ID, field)
				}
			}
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
