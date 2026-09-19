package sourcecomp

import (
	"fmt"

	extconfig "dynamic-runtime/extensions/config"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// 四层声明模型（features/20260919.md）：机台清单（事实）× 格式清单组
// （分配）× 格式清单（语义 = 一张表的契约）× sink 清单（连接）。装载时
// 展开为 机台 × 格式 的源定义——实例制语义不变：无证据不物化，缺失
// 可见性由巡检（expect）承担。

// FleetDefaults 是横切默认值，逐级可被格式/组/机台覆盖（本期实现的
// 覆盖点：expect / inspection_lookback_days / file_stable_window_seconds）。
type FleetDefaults struct {
	StateDir            string
	DatePolicy          string
	SpecificDate        string
	CatchupDays         int
	BatchSize           int
	StableWindowSeconds int
}

// SinkDef is one database connection in the sink inventory. Connection
// configuration is deliberately separated from data semantics: migrating a
// database means editing one sink entry, not N format definitions.
type SinkDef struct {
	Name           string
	Driver         string // sqlite | mysql | postgresql | oracle（存储家族）
	DriverOverride string // 可选：实际 sql driver 注册名（如测试桩）
	DSN            string
	FileTable      string // 可选：该 sink 的文件登记表（跨格式共享）
	LazyConnect    *bool
}

// FormatDef is the complete semantic contract of one data file family:
// what it looks like (match), how it parses (parser), where it lands
// (table + sink), and how its absence is judged (expect). One format = one
// table = one column contract.
type FormatDef struct {
	Name        string
	Match       string // 文件名签名（可含日期词表 YYYY/YY/MM/DD）
	Table       string
	Sink        string
	Parser      map[string]any
	Columns     []any
	MetadataRls []any          // path-metadata 规则（from=filename/path）
	Layout      string         // dated（默认）| flat（path 即单文件）
	Extra       map[string]any // 其余键原样透传进源配置（dedupe/collection_mode 等）

	// 派生：match 去掉日期词表后的 glob（目录发现的 pattern）。
	pattern      string
	hasDateToken bool
}

// FormatGroupDef assigns a set of formats to a class of machines. The group
// IS the machine type's collection scope — there is no separate type entity.
type FormatGroupDef struct {
	Name    string
	Formats []string
}

// MachineDef is one fleet fact row. Machines do not participate in table
// composition: they contribute rows (machine_no as a data column), and the
// path/since facts that bound where and when their data exists.
type MachineDef struct {
	No       string
	IP       string
	Path     string
	Group    string
	Since    string
	Metadata map[string]string
}

var dateTokens = []string{"YYYY", "YY", "MM", "DD"}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func hasDateToken(s string) bool {
	for _, t := range dateTokens {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// dateTokenAt 返回 s 处命中的日期记号（长记号优先），未命中返回空。
func dateTokenAt(s string) string {
	for _, t := range dateTokens {
		if strings.HasPrefix(s, t) {
			return t
		}
	}
	return ""
}

// patternFromMatch 把文件名签名转为目录发现 glob：连续的日期记号段
// 折叠为一个 *（"YYMMDD" 与 "YYYY" 都只产生一个 *）。
func patternFromMatch(match string) string {
	var b strings.Builder
	for i := 0; i < len(match); {
		if t := dateTokenAt(match[i:]); t != "" {
			b.WriteString("*")
			i += len(t)
			for {
				if t2 := dateTokenAt(match[i:]); t2 != "" {
					i += len(t2)
					continue
				}
				break
			}
			continue
		}
		b.WriteByte(match[i])
		i++
	}
	return b.String()
}

// splitDatePath 把机台 path 拆为静态根与日期目录段。日期词表可能同时
// 出现在 path（目录日期）与 match（文件名日期）——至少一处必须存在，
// 否则该组合无法归因业务日期。
func splitDatePath(p string) (root, dirLayout string, hasTokens bool) {
	for i := 0; i < len(p); i++ {
		rest := p[i:]
		for _, t := range dateTokens {
			if strings.HasPrefix(rest, t) {
				return strings.TrimRight(p[:i], `/\`), p[i:], true
			}
		}
	}
	return p, "", false
}

// nativeSep 与 app/source 的约定一致：配置里的反斜杠一律视为分隔符。
// 独立实现避免包间耦合；若两处语义漂移，fleet 冒烟测试会先红。
func nativeSep(p string) string {
	if p == "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return filepath.FromSlash(strings.ReplaceAll(p, "\\", "/"))
	}
	if !strings.ContainsRune(p, '\\') {
		return p
	}
	return strings.ReplaceAll(p, "\\", "/")
}

// ScheduleDef 是一条调度声明：cron 或每日 time 二选一，group 引用
// 格式清单组（该组机台按此节奏采集）。空 group = 全局广播。
type ScheduleDef struct {
	Cron  string
	Time  string
	Group string
}

// expandFleet 展开：机台 × 其组的格式 → 源定义。引用完整性在此强校验
// （缺格式/缺组/缺 sink/坏 since 任一 → 响亮失败，绝不静默丢采集）。
func expandFleet(d FleetDefaults, sinks []SinkDef, formats []FormatDef, groups []FormatGroupDef, machines []MachineDef, schedules []ScheduleDef) ([]*ResolvedSource, []extconfig.ComponentConfig, error) {
	sinkByName := map[string]SinkDef{}
	for _, sk := range sinks {
		if sk.Name == "" {
			return nil, nil, fmt.Errorf("sinks: name is required")
		}
		if _, dup := sinkByName[sk.Name]; dup {
			return nil, nil, fmt.Errorf("sinks: duplicate sink name %q", sk.Name)
		}
		sinkByName[sk.Name] = sk
	}
	formatByName := map[string]FormatDef{}
	for i := range formats {
		f := &formats[i]
		if f.Name == "" {
			return nil, nil, fmt.Errorf("formats #%d: name is required", i)
		}
		if _, dup := formatByName[f.Name]; dup {
			return nil, nil, fmt.Errorf("formats: duplicate format name %q", f.Name)
		}
		if f.Match == "" {
			return nil, nil, fmt.Errorf("format %q: match is required", f.Name)
		}
		if f.Sink == "" {
			return nil, nil, fmt.Errorf("format %q: sink reference is required", f.Name)
		}
		if _, ok := sinkByName[f.Sink]; !ok {
			return nil, nil, fmt.Errorf("format %q references unknown sink %q", f.Name, f.Sink)
		}
		f.hasDateToken = hasDateToken(f.Match)
		f.pattern = patternFromMatch(f.Match)
		formats[i] = *f
		formatByName[f.Name] = formats[i]
	}
	groupByName := map[string]FormatGroupDef{}
	for _, g := range groups {
		if g.Name == "" {
			return nil, nil, fmt.Errorf("format_groups: name is required")
		}
		if _, dup := groupByName[g.Name]; dup {
			return nil, nil, fmt.Errorf("format_groups: duplicate group name %q", g.Name)
		}
		if len(g.Formats) == 0 {
			return nil, nil, fmt.Errorf("format group %q: formats is required", g.Name)
		}
		for _, fname := range g.Formats {
			if _, ok := formatByName[fname]; !ok {
				return nil, nil, fmt.Errorf("format group %q references unknown format %q", g.Name, fname)
			}
		}
		groupByName[g.Name] = g
	}
	machineSeen := map[string]bool{}
	for _, m := range machines {
		if m.No == "" {
			return nil, nil, fmt.Errorf("machines: no is required")
		}
		if machineSeen[m.No] {
			return nil, nil, fmt.Errorf("machines: duplicate machine no %q", m.No)
		}
		machineSeen[m.No] = true
		if m.Path == "" {
			return nil, nil, fmt.Errorf("machine %q: path is required", m.No)
		}
		if _, ok := groupByName[m.Group]; !ok {
			return nil, nil, fmt.Errorf("machine %q references unknown format group %q", m.No, m.Group)
		}
	}

	out := make([]*ResolvedSource, 0, len(machines))
	for _, m := range machines {
		g := groupByName[m.Group]
		machinePath := nativeSep(strings.NewReplacer("{ip}", m.IP, "{no}", m.No).Replace(m.Path))
		root, dirLayout, pathHasTokens := splitDatePath(machinePath)
		for _, fname := range g.Formats {
			f := formatByName[fname]
			flatOK := f.Layout == "flat" || d.DatePolicy == "today"
			if !pathHasTokens && !f.hasDateToken && !flatOK && d.DatePolicy != "specific" {
				// 滚动策略（yesterday/daily）需要日期记号归因业务日；
				// specific 策略的日期归属来自策略本身，无需记号。
				return nil, nil, fmt.Errorf("machine %q + format %q: neither the machine path nor the match carries a date token — the business date cannot be attributed", m.No, fname)
			}
			cfg := map[string]any{
				"source_id":                  m.No + "-" + f.Name,
				"path":                       root,
				"pattern":                    f.pattern,
				"batch_size":                 d.BatchSize,
				"date_policy":                d.DatePolicy,
				"catchup_days":               d.CatchupDays,
				"file_stable_window_seconds": d.StableWindowSeconds,
				"state_dir":                  d.StateDir,
				"state_type":                 "file-state",
				"header":                     true,
				"storage":                    sinkByName[f.Sink].Driver,
				"dsn":                        sinkByName[f.Sink].DSN,
				"table":                      f.Table,
				"expose_console":             true,
				"lazy_connect":               true,
			}
			if dirLayout != "" {
				cfg["date_dir_layout"] = dirLayout
			}
			if f.hasDateToken {
				cfg["filename_date_layout"] = f.Match
			}
			// 内容探测模式：发现以内容判定，glob 必须为空（校验约束）。
			if dc, ok := f.Parser["detect_content"].(bool); ok && dc {
				delete(cfg, "pattern")
				delete(cfg, "filename_date_layout")
			}
			if sinkByName[f.Sink].FileTable != "" {
				cfg["file_table"] = sinkByName[f.Sink].FileTable
			}
			if sinkByName[f.Sink].LazyConnect != nil {
				cfg["lazy_connect"] = *sinkByName[f.Sink].LazyConnect
			}
			if ov := sinkByName[f.Sink].DriverOverride; ov != "" {
				cfg["driver"] = ov
			}
			// 解析器设置透传（encoding/header/allow_ragged/delimiter/skip_lines）。
			for k, v := range f.Parser {
				cfg[k] = v
			}
			if len(f.Columns) > 0 {
				cfg["columns"] = f.Columns
			}
			if len(f.MetadataRls) > 0 {
				cfg["path_metadata"] = f.MetadataRls
			}
			if m.Since != "" {
				cfg["since"] = m.Since
			}
			if d.SpecificDate != "" {
				cfg["specific_date"] = d.SpecificDate
			}
			if f.Layout != "" {
				cfg["layout"] = f.Layout
			}
			for k, v := range f.Extra {
				cfg[k] = v
			}
			meta := map[string]string{
				"machine_no": m.No,
				"format":     f.Name,
			}
			if m.IP != "" {
				meta["ip"] = m.IP
			}
			for k, v := range m.Metadata {
				meta[k] = v
			}
			out = append(out, &ResolvedSource{
				ID:       m.No + "-" + f.Name,
				Path:     root,
				Profiles: []string{g.Name},
				Metadata: meta,
				Config:   cfg,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	// 调度声明 → 单个 scheduler 组件行（exclusive provider 只允许一个
	// scheduler；多条目由 [[schedules]] 数组承载，每条 group 定向）。
	var schedEntries []any
	seen := map[string]bool{}
	for i, sc := range schedules {
		if sc.Cron == "" && sc.Time == "" {
			return nil, nil, fmt.Errorf("schedules #%d: cron or time is required", i)
		}
		if sc.Group != "" {
			if _, ok := groupByName[sc.Group]; !ok {
				return nil, nil, fmt.Errorf("schedules #%d references unknown format group %q", i, sc.Group)
			}
		}
		id := sc.Group
		if id == "" {
			id = fmt.Sprintf("sched-%d", i)
		}
		if seen[id] {
			return nil, nil, fmt.Errorf("schedules: duplicate group %q", sc.Group)
		}
		seen[id] = true
		e := map[string]any{}
		if sc.Cron != "" {
			e["cron"] = sc.Cron
		} else {
			e["time"] = sc.Time
		}
		if sc.Group != "" {
			e["group"] = sc.Group
		}
		schedEntries = append(schedEntries, e)
	}
	var rows []extconfig.ComponentConfig
	if len(schedEntries) > 0 {
		rows = append(rows, extconfig.ComponentConfig{
			ID:   "scheduler",
			Type: "scheduler",
			Config: map[string]any{
				"schedules": schedEntries,
			},
		})
	}

	return out, rows, nil
}

// ResolvedSource is the runtime-visible form of one expanded (machine,
// format) source: the merged configuration the source-unit component parses.
type ResolvedSource struct {
	ID       string
	Path     string
	Profiles []string
	Metadata map[string]string
	Config   map[string]any
}

// SourceView is the stable configuration description used by validation and
// the Explorer UI.
func (s *ResolvedSource) SourceView() map[string]any {
	out := map[string]any{
		"id":       s.ID,
		"path":     s.Path,
		"groups":   append([]string(nil), s.Profiles...),
		"metadata": copyStringMap(s.Metadata),
	}
	for k, v := range s.Config {
		out[k] = v
	}
	return out
}

func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func parseFleetDefaults(raw any) (FleetDefaults, error) {
	d := FleetDefaults{DatePolicy: "yesterday"}
	m, ok := raw.(map[string]any)
	if !ok {
		if raw == nil {
			return d, nil
		}
		return d, fmt.Errorf("defaults must be a table ([defaults])")
	}
	d.StateDir, _ = m["state_dir"].(string)
	d.DatePolicy, _ = m["date_policy"].(string)
	if v, ok := m["specific_date"].(string); ok {
		d.SpecificDate = v
	}
	if v, ok := m["catchup_days"].(int64); ok {
		d.CatchupDays = int(v)
	}
	if v, ok := m["batch_size"].(int64); ok {
		d.BatchSize = int(v)
	}
	if v, ok := m["file_stable_window_seconds"].(int64); ok {
		d.StableWindowSeconds = int(v)
	}
	return d, nil
}

func parseSinks(raw any) ([]SinkDef, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("sinks must be an array of tables ([[sinks]])")
	}
	out := make([]SinkDef, 0, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sinks #%d must be a table", i)
		}
		sk := SinkDef{}
		sk.Name, _ = m["name"].(string)
		sk.Driver, _ = m["driver"].(string)
		sk.DriverOverride, _ = m["driver_override"].(string)
		sk.DSN, _ = m["dsn"].(string)
		sk.FileTable, _ = m["file_table"].(string)
		if v, ok := m["lazy_connect"].(bool); ok {
			sk.LazyConnect = &v
		}
		if sk.Name == "" || sk.Driver == "" {
			return nil, fmt.Errorf("sinks #%d: name/driver are required", i)
		}
		out = append(out, sk)
	}
	return out, nil
}

func parseFormats(raw any) ([]FormatDef, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("formats must be an array of tables ([[formats]])")
	}
	out := make([]FormatDef, 0, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("formats #%d must be a table", i)
		}
		f := FormatDef{}
		f.Name, _ = m["name"].(string)
		f.Match, _ = m["match"].(string)
		f.Table, _ = m["table"].(string)
		f.Sink, _ = m["sink"].(string)
		if pm, ok := m["parser"].(map[string]any); ok {
			f.Parser = pm
		}
		if cs, ok := m["columns"].([]any); ok {
			f.Columns = cs
		}
		if md, ok := m["metadata"].([]any); ok {
			f.MetadataRls = md
		}
		if l, ok := m["layout"].(string); ok {
			f.Layout = l
		}
		known := map[string]bool{
			"name": true, "match": true, "table": true, "sink": true, "expect": true,
			"inspection_lookback_days": true, "parser": true, "columns": true,
			"metadata": true, "layout": true,
		}
		for k, v := range m {
			if !known[k] {
				if f.Extra == nil {
					f.Extra = map[string]any{}
				}
				f.Extra[k] = v
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func parseFormatGroups(raw any) ([]FormatGroupDef, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("format_groups must be an array of tables")
	}
	out := make([]FormatGroupDef, 0, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("format_groups #%d must be a table", i)
		}
		g := FormatGroupDef{}
		g.Name, _ = m["name"].(string)
		if fs, ok := m["formats"].([]any); ok {
			for _, f := range fs {
				if s, ok := f.(string); ok {
					g.Formats = append(g.Formats, s)
				}
			}
		}
		out = append(out, g)
	}
	return out, nil
}

func parseMachines(raw any) ([]MachineDef, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("machines must be an array of tables ([[machines]])")
	}
	out := make([]MachineDef, 0, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("machines #%d must be a table", i)
		}
		md := MachineDef{}
		md.No, _ = m["no"].(string)
		md.IP, _ = m["ip"].(string)
		md.Path, _ = m["path"].(string)
		md.Group, _ = m["group"].(string)
		md.Since, _ = m["since"].(string)
		if meta, ok := m["metadata"].(map[string]any); ok {
			md.Metadata = map[string]string{}
			for k, v := range meta {
				if s, ok := v.(string); ok {
					md.Metadata[k] = s
				}
			}
		}
		out = append(out, md)
	}
	return out, nil
}

func parseSchedules(raw any) ([]ScheduleDef, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("schedules must be an array of tables ([[schedules]])")
	}
	out := make([]ScheduleDef, 0, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schedules #%d must be a table", i)
		}
		sd := ScheduleDef{}
		sd.Cron, _ = m["cron"].(string)
		sd.Time, _ = m["time"].(string)
		sd.Group, _ = m["group"].(string)
		out = append(out, sd)
	}
	return out, nil
}
