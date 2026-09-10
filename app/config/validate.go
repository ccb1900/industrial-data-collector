package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	extconfig "dynamic-runtime/extensions/config"

	appencoding "gocordis-csv-collector/app/encoding"
	appmetadata "gocordis-csv-collector/app/metadata"
	appparser "gocordis-csv-collector/app/parser"
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
	"text-parser":        {Kind: "parser", Capability: "csvparser", Name: "Text Parser"},
	"single-file-source": {Kind: "source", Capability: "filesource", Name: "Single File Source"},
	"watch-file-trigger": {Kind: "watch-trigger", Capability: "watch-trigger", Name: "File Watch Trigger"},
	"memory-storage":     {Kind: "storage", Capability: "storage", Name: "Memory Storage"},
	"mysql-storage":      {Kind: "storage", Capability: "storage", Name: "MySQL Storage"},
	"postgresql-storage": {Kind: "storage", Capability: "storage", Name: "PostgreSQL Storage"},
	"oracle-storage":     {Kind: "storage", Capability: "storage", Name: "Oracle Storage"},
	"sqlite-storage":     {Kind: "storage", Capability: "storage", Name: "SQLite Storage"},
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
	"csv-source-unit":    {Kind: "source-unit", Capability: "source-unit", Name: "CSV Source Unit"},
	"console-bridge":     {Kind: "console-bridge", Capability: "console-bridge", Name: "Console Bridge"},
	"console-rows":       {Kind: "console-bridge", Capability: "console-rows", Name: "Console Rows"},
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
			if knownTypes[target.Type].Kind != "source" && knownTypes[target.Type].Kind != "source-unit" {
				return fmt.Errorf("metadata component %q source %q must reference a source component", id, ref)
			}
			targetRoot := str(target.Config, "root")
			if targetRoot == "" {
				targetRoot = str(target.Config, "path")
			}
			if targetRoot != set.Root {
				return fmt.Errorf("metadata component %q source %q root %q does not match source root %q", id, ref, set.Root, targetRoot)
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
		if cc.Type == "single-file-source" {
			if str(cc.Config, "path") == "" {
				return fmt.Errorf("single-file-source %q missing path", cc.ID)
			}
			if raw, ok := cc.Config["dedupe_content_hash"]; ok {
				if _, valid := boolCfgValue(raw); !valid {
					return fmt.Errorf("single-file-source %q dedupe_content_hash must be a boolean", cc.ID)
				}
			}
			break
		}
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
		if err := validateDetectContent(cc, "source"); err != nil {
			return err
		}
		if _, err := appencoding.Normalize(str(cc.Config, "encoding")); err != nil {
			return fmt.Errorf("source %q: %v", cc.ID, err)
		}
	case "parser":
		if _, err := appencoding.Normalize(str(cc.Config, "encoding")); err != nil {
			return fmt.Errorf("parser %q: %v", cc.ID, err)
		}
		if cc.Type == "text-parser" {
			format := str(cc.Config, "text_format")
			if format == "" {
				format = "single-value"
			}
			switch format {
			case "single-value", "line-regex", "key-value":
			default:
				return fmt.Errorf("text-parser %q unknown text_format %q", cc.ID, format)
			}
			if format == "line-regex" && str(cc.Config, "pattern") == "" {
				return fmt.Errorf("text-parser %q line-regex requires pattern", cc.ID)
			}
			break
		}
		skip := 0
		if raw, ok := cc.Config["skip_lines"]; ok {
			n, valid := intCfgValue(raw)
			if !valid {
				return fmt.Errorf("parser %q skip_lines must be an integer", cc.ID)
			}
			if n < 0 {
				return fmt.Errorf("parser %q skip_lines must be >= 0", cc.ID)
			}
			skip = n
		}
		header := true
		if raw, ok := cc.Config["header"]; ok {
			b, valid := boolCfgValue(raw)
			if !valid {
				return fmt.Errorf("parser %q header must be a boolean", cc.ID)
			}
			header = b
		}
		docCfg, err := appparser.ParseDocumentConfig(cc.Config)
		if err != nil {
			return fmt.Errorf("parser %q: %v", cc.ID, err)
		}
		if docCfg.Enabled() && !header {
			return fmt.Errorf("parser %q structured csv.metadata mode requires header=true", cc.ID)
		}
		if docCfg.Enabled() && skip != 0 {
			return fmt.Errorf("parser %q structured csv.metadata mode cannot be combined with skip_lines", cc.ID)
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
		if policy != "yesterday" && policy != "specific" && policy != "today" {
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
		if raw, ok := cc.Config["catchup_days"]; ok {
			d, valid := intCfgValue(raw)
			if !valid || d < 0 {
				return fmt.Errorf("collector %q catchup_days must be a non-negative integer", cc.ID)
			}
		}
	case "source-unit":
		if err := validateSourceUnit(cc); err != nil {
			return err
		}
	case "console-bridge", "console-rows":
		// no config keys in v0.2
	case "watch-trigger":
		if str(cc.Config, "path") == "" {
			return fmt.Errorf("watch-file-trigger %q missing path", cc.ID)
		}
		if str(cc.Config, "source") == "" {
			return fmt.Errorf("watch-file-trigger %q missing source", cc.ID)
		}
		if raw := str(cc.Config, "debounce"); raw != "" {
			if _, err := time.ParseDuration(raw); err != nil {
				return fmt.Errorf("watch-file-trigger %q debounce invalid: %v", cc.ID, err)
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

func boolCfgValue(raw any) (bool, bool) {
	switch b := raw.(type) {
	case bool:
		return b, true
	case string:
		v, err := strconv.ParseBool(b)
		return v, err == nil
	default:
		v, err := strconv.ParseBool(fmt.Sprint(raw))
		return v, err == nil
	}
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

func validateSourceUnit(cc extconfig.ComponentConfig) error {
	if str(cc.Config, "source_id") == "" {
		return fmt.Errorf("source-unit %q missing source_id", cc.ID)
	}
	if str(cc.Config, "path") == "" {
		return fmt.Errorf("source-unit %q missing path", cc.ID)
	}
	if raw, ok := cc.Config["file_stable_window_seconds"]; ok {
		w, valid := intCfgValue(raw)
		if !valid || w < 0 {
			return fmt.Errorf("source-unit %q file_stable_window_seconds must be a non-negative integer", cc.ID)
		}
	}
	if err := validateDetectContent(cc, "source-unit"); err != nil {
		return err
	}
	if _, err := appencoding.Normalize(str(cc.Config, "encoding")); err != nil {
		return fmt.Errorf("source-unit %q: %v", cc.ID, err)
	}
	kind := str(cc.Config, "parser")
	if kind == "" {
		kind = "csv"
	}
	if kind == "text" || kind == "text-parser" {
		format := str(cc.Config, "text_format")
		if format == "" {
			format = "single-value"
		}
		switch format {
		case "single-value", "line-regex", "key-value":
		default:
			return fmt.Errorf("source-unit %q unknown text_format %q", cc.ID, format)
		}
		if format == "line-regex" && str(cc.Config, "pattern") == "" {
			return fmt.Errorf("source-unit %q line-regex requires pattern", cc.ID)
		}
	} else if kind != "csv" && kind != "csv-parser" {
		return fmt.Errorf("source-unit %q unsupported parser %q", cc.ID, kind)
	}
	if l := str(cc.Config, "layout"); l != "" && l != "dated" && l != "flat" {
		return fmt.Errorf("source-unit %q layout must be dated or flat", cc.ID)
	}
	if raw, ok := cc.Config["dedupe_content_hash"]; ok {
		if _, valid := boolCfgValue(raw); !valid {
			return fmt.Errorf("source-unit %q dedupe_content_hash must be a boolean", cc.ID)
		}
	}
	if mode := str(cc.Config, "collection_mode"); mode != "" && mode != "batch" && mode != "append" {
		return fmt.Errorf("source-unit %q collection_mode must be batch or append", cc.ID)
	}
	header := true
	if raw, ok := cc.Config["header"]; ok {
		b, valid := boolCfgValue(raw)
		if !valid {
			return fmt.Errorf("source-unit %q header must be a boolean", cc.ID)
		}
		header = b
	}
	skip := 0
	if raw, ok := cc.Config["skip_lines"]; ok {
		n, valid := intCfgValue(raw)
		if !valid || n < 0 {
			return fmt.Errorf("source-unit %q skip_lines must be a non-negative integer", cc.ID)
		}
		skip = n
	}
	if raw, ok := cc.Config["delimiter"]; ok {
		d := fmt.Sprint(raw)
		if len([]rune(d)) != 1 {
			return fmt.Errorf("source-unit %q delimiter must be one character", cc.ID)
		}
	}
	docCfg, err := appparser.ParseDocumentConfig(cc.Config)
	if err != nil {
		return fmt.Errorf("source-unit %q: %v", cc.ID, err)
	}
	if docCfg.Enabled() && !header {
		return fmt.Errorf("source-unit %q structured csv.metadata mode requires header=true", cc.ID)
	}
	if docCfg.Enabled() && skip != 0 {
		return fmt.Errorf("source-unit %q structured csv.metadata mode cannot be combined with skip_lines", cc.ID)
	}

	stateType := str(cc.Config, "state_type")
	if stateType == "" {
		stateType = "memory-state"
	}
	if stateType != "memory-state" && stateType != "file-state" {
		return fmt.Errorf("source-unit %q unknown state_type %q", cc.ID, stateType)
	}
	if stateType == "file-state" && str(cc.Config, "state_dir") == "" {
		return fmt.Errorf("source-unit %q file-state requires state_dir", cc.ID)
	}

	storageType := str(cc.Config, "storage")
	if storageType == "" {
		storageType = str(cc.Config, "sink")
	}
	if storageType == "" {
		storageType = "memory-storage"
	}
	storageType = strings.ToLower(storageType)
	switch storageType {
	case "memory", "memory-storage", "mysql", "mysql-storage", "postgres", "postgresql", "postgresql-storage", "oracle", "oracle-storage", "sqlite", "sqlite-storage":
	default:
		return fmt.Errorf("source-unit %q unknown storage type %q", cc.ID, storageType)
	}
	if storageType != "memory" && storageType != "memory-storage" {
		if str(cc.Config, "dsn") == "" {
			return fmt.Errorf("source-unit %q storage requires dsn", cc.ID)
		}
		table := str(cc.Config, "table")
		if table == "" {
			table = "gocordis_records"
		}
		if !tableNameRE.MatchString(table) {
			return fmt.Errorf("source-unit %q table is not a simple identifier", cc.ID)
		}
	}

	policy := str(cc.Config, "date_policy")
	if policy == "" {
		policy = "yesterday"
	}
	if policy != "yesterday" && policy != "specific" && policy != "today" {
		return fmt.Errorf("source-unit %q invalid date_policy %q", cc.ID, policy)
	}
	if policy == "specific" {
		if _, err := time.Parse("2006-01-02", str(cc.Config, "specific_date")); err != nil {
			return fmt.Errorf("source-unit %q specific_date invalid", cc.ID)
		}
	}
	if raw, ok := cc.Config["batch_size"]; ok {
		b, valid := intCfgValue(raw)
		if !valid || b <= 0 {
			return fmt.Errorf("source-unit %q batch_size must be a positive integer", cc.ID)
		}
	}
	if raw, ok := cc.Config["catchup_days"]; ok {
		d, valid := intCfgValue(raw)
		if !valid || d < 0 {
			return fmt.Errorf("source-unit %q catchup_days must be a non-negative integer", cc.ID)
		}
	}
	if raw, ok := cc.Config["lazy_connect"]; ok {
		if _, valid := boolCfgValue(raw); !valid {
			return fmt.Errorf("source-unit %q lazy_connect must be a boolean", cc.ID)
		}
	}
	if _, err := appmetadata.ParseRules(cc.Config["path_metadata"]); err != nil {
		return fmt.Errorf("source-unit %q path_metadata: %w", cc.ID, err)
	}
	return nil
}

// validateDetectContent rejects ambiguous discovery configuration: content
// detection judges every file by its bytes, so a name glob alongside it has
// no defined meaning.
func validateDetectContent(cc extconfig.ComponentConfig, kind string) error {
	if raw, ok := cc.Config["detect_content"]; ok {
		if _, valid := boolCfgValue(raw); !valid {
			return fmt.Errorf("%s %q detect_content must be a boolean", kind, cc.ID)
		}
		if valid, _ := boolCfgValue(raw); valid && str(cc.Config, "pattern") != "" {
			return fmt.Errorf("%s %q pattern must be empty when detect_content is true", kind, cc.ID)
		}
	}
	return nil
}

// ConsoleCritical reports whether a component type is part of the console
// infrastructure itself. Uninstalling one would tear down the console the
// operator is using, so lifecycle requests refuse them (mirrors the
// protected set of the plugin explorer's Control path).
func ConsoleCritical(typ string) bool {
	switch typ {
	case "ui", "query-provider", "console-bridge", "plugin-explorer":
		return true
	}
	return false
}

func AllowedSourceType(typ string) bool {
	return typ == "local-file-source" || typ == "unc-file-source"
}
