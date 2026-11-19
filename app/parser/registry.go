package parser

// parser 注册表：新增一种文件格式 = 在本包注册一个工厂函数，sourceunit 与
// 配置校验都不再需要各自的 switch。这是 Iteration F 的"新增第四种格式不改
// sourceunit"验收点的实现载体。
//
// 注册发生在各实现的 init() 里（csv / text 内置于本包）；测试可以用
// UnregisterParser 注销替身。

import (
	"fmt"
	"sort"
	"sync"

	"dynamic-runtime/extensions/config"
	"dynamic-runtime/extensions/console/configutil"

	appencoding "gocordis-csv-collector/app/encoding"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// ParserFactory 从组件配置（扁平键值）构造一个 parser。
type ParserFactory func(cfg map[string]any) (model.CSVParser, error)

var (
	parserMu    sync.RWMutex
	parserKinds = map[string]ParserFactory{}
)

// RegisterParser 注册一种 parser 取值。同名重复注册报错：两种实现抢同一个
// 配置键是装配错误，绝不静默后来者赢。
func RegisterParser(kind string, f ParserFactory) error {
	if kind == "" || f == nil {
		return fmt.Errorf("parser registry: kind and factory must not be empty")
	}
	parserMu.Lock()
	defer parserMu.Unlock()
	if _, dup := parserKinds[kind]; dup {
		return fmt.Errorf("parser registry: kind %q already registered", kind)
	}
	parserKinds[kind] = f
	return nil
}

// UnregisterParser 只供测试注销替身；生产代码不调用。
func UnregisterParser(kind string) {
	parserMu.Lock()
	defer parserMu.Unlock()
	delete(parserKinds, kind)
}

// ParserFor 按取值查找工厂。取值就是配置里的 parser 键原文——别名
// （csv-parser/text-parser）以独立条目注册，不做隐藏的规范化。
func ParserFor(kind string) (ParserFactory, bool) {
	parserMu.RLock()
	defer parserMu.RUnlock()
	f, ok := parserKinds[kind]
	return f, ok
}

// KnownParserKinds 返回全部已注册取值（排序），配置校验用它拒绝未知取值——
// 校验白名单与运行时分派从此同源，不会再出现"校验放行、装配报错"的裂缝。
func KnownParserKinds() []string {
	parserMu.RLock()
	defer parserMu.RUnlock()
	out := make([]string, 0, len(parserKinds))
	for k := range parserKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func init() {
	if err := RegisterParser("csv", NewCSVFromConfigValues); err != nil {
		panic(err)
	}
	if err := RegisterParser("csv-parser", NewCSVFromConfigValues); err != nil {
		panic(err)
	}
	if err := RegisterParser("text", func(cfg map[string]any) (model.CSVParser, error) {
		return NewTextParserFromConfigValues(cfg)
	}); err != nil {
		panic(err)
	}
	if err := RegisterParser("text-parser", func(cfg map[string]any) (model.CSVParser, error) {
		return NewTextParserFromConfigValues(cfg)
	}); err != nil {
		panic(err)
	}
}

// NewCSVFromConfigValues 从扁平配置构造 CSV parser：编码、表头、跳行、锯齿
// 放行、分隔符，以及结构化 csv.metadata 段的跨字段约束。原 sourceunit
// buildParser 的 csv 分支整体迁入——它本来就是这个配置面的所有者。
// 错误类与文案逐字保留（errs.ErrInvalidConfig + 原前缀），迁移不改变任何
// 调用方看到的错误。
func NewCSVFromConfigValues(cfg map[string]any) (model.CSVParser, error) {
	cc := config.ComponentConfig{Config: cfg}
	p := New()
	encName, encErr := appencoding.Normalize(configutil.OptionalString(cc, "encoding", "utf8"))
	if encErr != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser: %v", encErr)
	}
	p.Encoding = encName
	p.Header = configutil.OptionalBool(cc, "header", true)
	p.SkipLines = configutil.OptionalInt(cc, "skip_lines", 0)
	if p.SkipLines < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser skip_lines must be >= 0")
	}
	p.AllowRagged = configutil.OptionalBool(cc, "allow_ragged", false)
	if d := configutil.OptionalString(cc, "delimiter", ""); d != "" {
		runes := []rune(d)
		if len(runes) != 1 {
			return nil, errs.Sourcef(errs.ErrInvalidConfig, "delimiter must be one character")
		}
		p.Comma = runes[0]
	}
	docCfg, err := ParseDocumentConfig(cfg)
	if err != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser: %v", err)
	}
	// 结构化元数据段要求行与声明的字段一一对应，锯齿放行会让元数据错位——
	// 显式拒绝而不是静默忽略 allow_ragged。
	if docCfg.Enabled() && !p.Header {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode requires header=true")
	}
	if docCfg.Enabled() && p.AllowRagged {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode does not support allow_ragged")
	}
	if docCfg.Enabled() && p.SkipLines != 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode cannot be combined with skip_lines")
	}
	p.Document = docCfg
	return p, nil
}
