package config

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/robfig/cron/v3"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	extconfig "dynamic-runtime/extensions/config"

	"gocordis-csv-collector/internal/pluginmeta"

	appencoding "gocordis-csv-collector/app/encoding"
	appmetadata "gocordis-csv-collector/app/metadata"
	appparser "gocordis-csv-collector/app/parser"
)

// knownTypes aggregates the per-package manifests: every built-in component
// package embeds its own manifest.toml (go:embed, registered in the
// package's init into internal/pluginmeta), so this table is generated from
// the packages — never hand-maintained.
func knownTypes() map[string]pluginmeta.TypeInfo {
	return pluginmeta.Types()
}

// DisplayName returns the human-facing plugin label for a known component
// type. Unknown/empty values fall back to the raw type.
func DisplayName(typ string) string {
	if ti, ok := pluginmeta.DisplayName(typ); ok {
		return ti
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
		ti, ok := knownTypes()[cc.Type]
		if !ok {
			return fmt.Errorf("unknown component type %q", cc.Type)
		}
		if err := validateSQLDriver(cc); err != nil {
			return err
		}
		if err := validateOne(cfg, cc, ti); err != nil {
			return err
		}
	}
	// 类型化入库的交叉防御：同一 (dsn, table) 被多个源单元声明时，列集
	// 必须完全一致。"多源同表"（同列、靠 source_id 区分）是合法模式；
	// 列集不同则一定是配错——打错一个表名就把两种业务静默写进一张表。
	type tableKey struct{ dsn, table string }
	typed := map[tableKey]struct{ id, sig string }{}
	for id, cc := range byID {
		if cc.Type != "csv-source-unit" || cc.Config["columns"] == nil {
			continue
		}
		sig, err := columnSignature(cc.Config["columns"])
		if err != nil {
			return fmt.Errorf("source %q: %v", id, err)
		}
		k := tableKey{dsn: str(cc.Config, "dsn"), table: str(cc.Config, "table")}
		if k.table == "" {
			k.table = "records"
		}
		if prev, ok := typed[k]; ok {
			if prev.sig != sig {
				return fmt.Errorf("sources %q and %q declare the same table %q (dsn %q) with different column declarations — rename one table", prev.id, id, k.table, k.dsn)
			}
			continue
		}
		typed[k] = struct{ id, sig string }{id, sig}
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
			if knownTypes()[target.Type].Kind != "source" && knownTypes()[target.Type].Kind != "source-unit" {
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
			if knownTypes()[target.Type].Kind != want {
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

func validateOne(cfg extconfig.Config, cc extconfig.ComponentConfig, ti pluginmeta.TypeInfo) error {
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
		// 多条目 [[schedules]]：每条独立时间线（cron 或 daily+time），
		// 各带可选 group（机台组定向触发）。旧单键 cron/schedule/time
		// 等价于一条无组条目。
		if rows, ok := cc.Config["schedules"].([]any); ok && len(rows) > 0 {
			if cc.Config["cron"] != nil || cc.Config["schedule"] != nil || cc.Config["time"] != nil {
				return fmt.Errorf("scheduler %q: schedules 与 cron/schedule/time 不可混用", cc.ID)
			}
			for i, r := range rows {
				m, ok := r.(map[string]any)
				if !ok {
					return fmt.Errorf("scheduler %q: schedules #%d must be a table", cc.ID, i)
				}
				if cronExpr := str(m, "cron"); cronExpr != "" {
					if _, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(cronExpr); err != nil {
						return fmt.Errorf("scheduler %q: schedules #%d: invalid cron expression %q: %v", cc.ID, i, cronExpr, err)
					}
					continue
				}
				clock := str(m, "time")
				if clock == "" {
					clock = "02:00"
				}
				if _, err := parseClock(clock); err != nil {
					return fmt.Errorf("scheduler %q: schedules #%d: %w", cc.ID, i, err)
				}
			}
			return nil
		}
		if cronExpr := str(cc.Config, "cron"); cronExpr != "" {
			if _, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(cronExpr); err != nil {
				return fmt.Errorf("scheduler %q: invalid cron expression %q: %v", cc.ID, cronExpr, err)
			}
			return nil
		}
		if kind := str(cc.Config, "schedule"); kind != "daily" && kind != "" {
			return fmt.Errorf("scheduler %q must use schedule=\"daily\" (or a cron expression)", cc.ID)
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
		// 桥/行查询依赖控制台宿主（hub）与查询能力：三者必须同时组合，
		// 否则组件永远无法就绪。
		for _, req := range []string{"ui", "query-provider", "scheduler"} {
			if !hasType(cfg, req) {
				return fmt.Errorf("%s %q requires component %q in the composition", cc.Type, cc.ID, req)
			}
		}
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
// hasType reports whether the desired composition contains a component of
// the given type (ID matching is wrong here: scheduler ids vary per host).
func hasType(cfg extconfig.Config, typ string) bool {
	for _, cc := range cfg.Components {
		if cc.Type == typ {
			return true
		}
	}
	return false
}

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

// sqlDriverByType: 每个 SQL 存储类型期望的 database/sql 驱动名（与
// components/storage 的驱动注册一致）。mysql/postgres 需要在构建时引入
// 对应驱动包；oracle/sqlite 已内建。
var sqlDriverByType = map[string]string{
	"mysql-storage":      "mysql",
	"postgresql-storage": "pgx",
	"oracle-storage":     "oracle",
	"sqlite-storage":     "sqlite",
}

// validateSQLDriver 把"驱动未注册"的失败从运行时首次写库提前到配置校验：
// 驱动通过 blank import 注册进二进制，校验只查注册表，不发起连接。
func validateSQLDriver(cc extconfig.ComponentConfig) error {
	want, ok := sqlDriverByType[cc.Type]
	if !ok {
		// source-unit 行：入库驱动随 sink 画像合并在源配置里。
		if cc.Type != "csv-source-unit" {
			return nil
		}
		want = ""
	}
	driver := want
	if raw, ok := cc.Config["driver"].(string); ok && raw != "" {
		driver = raw
	}
	if driver == "" {
		return nil
	}
	for _, registered := range sql.Drivers() {
		if registered == driver {
			return nil
		}
	}
	return fmt.Errorf("component %q: SQL driver %q is not registered in this binary — import the driver package at build time (e.g. mysql-storage 需引入 github.com/go-sql-driver/mysql)", cc.ID, driver)
}

// columnSignature 提取列声明的列名集合指纹（排序后连接），用于同表
// 列集一致性比对。列名缺失视为配置错误（typed 模式必须有列名）。
func columnSignature(raw any) (string, error) {
	list, ok := raw.([]any)
	if !ok {
		return "", fmt.Errorf("columns must be an array of tables")
	}
	names := make([]string, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return "", fmt.Errorf("columns #%d must be a table", i)
		}
		name := str(m, "column")
		if name == "" {
			return "", fmt.Errorf("columns #%d missing column name", i)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "\x00"), nil
}
