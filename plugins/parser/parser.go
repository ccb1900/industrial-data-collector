package parserplugin

import (
	"dynamic-runtime/extensions/config"
	"dynamic-runtime/runtime"

	appencoding "gocordis-csv-collector/app/encoding"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/parser"
	"gocordis-csv-collector/plugins/internal/configutil"
)

// Key is the CSVParser capability exposed by this plugin.
var Key = runtime.NewKey[model.CSVParser]("csv.parser")

type ParserComponent struct {
	cfg model.CSVParser
}

func (c *ParserComponent) Name() string                 { return "csv-parser" }
func (c *ParserComponent) Inject() []runtime.Dependency { return nil }
func (c *ParserComponent) Provide() []runtime.Capability {
	return []runtime.Capability{Key.Capability()}
}
func (c *ParserComponent) Apply(ctx *runtime.Context) (runtime.Cleanup, error) {
	if err := runtime.Provide(ctx, Key, c.cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

// NewParser creates the parser Component from configuration. The csv-parser
// type builds the streaming CSV parser; the text-parser type builds one of
// the plain-text variants (single-value / line-regex / key-value).
func NewParser(cc config.ComponentConfig) (*ParserComponent, error) {
	if cc.Type == "text-parser" {
		tp, err := parser.NewTextParserFromConfigValues(cc.Config)
		if err != nil {
			return nil, err
		}
		return &ParserComponent{cfg: tp}, nil
	}
	header := configutil.OptionalBool(cc, "header", true)
	skipLines := configutil.OptionalInt(cc, "skip_lines", 0)
	if skipLines < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser skip_lines must be >= 0")
	}
	docCfg, err := parser.ParseDocumentConfig(cc.Config)
	if err != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser: %v", err)
	}
	if docCfg.Enabled() && !header {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode requires header=true")
	}
	if docCfg.Enabled() && skipLines != 0 {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "structured csv.metadata mode cannot be combined with skip_lines")
	}
	encoding, err := appencoding.Normalize(configutil.OptionalString(cc, "encoding", "utf8"))
	if err != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "parser: %v", err)
	}
	p := parser.New()
	p.Header = header
	p.SkipLines = skipLines
	p.Document = docCfg
	p.Encoding = encoding
	return &ParserComponent{cfg: p}, nil
}
