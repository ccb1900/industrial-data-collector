package parser

import (
	"bufio"
	"context"
	"io"
	"regexp"
	"sort"
	"strings"

	"dynamic-runtime/extensions/console/configutil"
	"dynamic-runtime/extensions/config"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// Text parser formats.
const (
	TextSingleValue = "single-value" // whole trimmed content is one value
	TextLineRegex   = "line-regex"   // one record per line via a named-group regex
	TextKeyValue    = "key-value"    // key=value lines; one record per snapshot
)

// TextParser parses plain-text files that are not CSV: instrument exports,
// single readings, key=value snapshots. It implements model.CSVParser so any
// sink/storage consumes it unchanged. Encoding is expected to be UTF-8.
//
//   - single-value: the trimmed file content is one field named ValueName.
//   - line-regex:   every non-empty line must match Pattern; named capture
//     groups become the columns. A non-matching line fails the file when
//     Strict is set and is skipped otherwise.
//   - key-value:    `key<Separator>value` lines; one record per file whose
//     columns are the keys in first-seen order.
type TextParser struct {
	Format    string
	Pattern   string
	ValueName string
	Separator string
	Strict    bool

	re *regexp.Regexp
}

func NewText(format string) (*TextParser, error) {
	switch format {
	case TextSingleValue, TextLineRegex, TextKeyValue:
	default:
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "text parser: unknown format %q (supported: single-value, line-regex, key-value)", format)
	}
	return &TextParser{Format: format, ValueName: "value", Separator: "="}, nil
}

// Compile validates and pre-compiles the line-regex pattern.
func (p *TextParser) Compile() error {
	if p.Format != TextLineRegex {
		return nil
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return errs.Sourcef(errs.ErrInvalidConfig, "text parser: line-regex requires pattern")
	}
	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return errs.Sourcef(errs.ErrInvalidConfig, "text parser: pattern: %v", err)
	}
	hasNames := false
	for _, n := range re.SubexpNames() {
		if n != "" {
			hasNames = true
			break
		}
	}
	if !hasNames {
		return errs.Sourcef(errs.ErrInvalidConfig, "text parser: line-regex pattern needs at least one named group (?<name>...)")
	}
	p.re = re
	return nil
}

func (p *TextParser) Parse(ctx context.Context, r io.Reader) (model.CSVDocument, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidFile, "nil reader")
	}
	var header []string
	var rows []model.Record
	switch p.Format {
	case TextSingleValue:
		name := p.ValueName
		if name == "" {
			name = "value"
		}
		content, err := io.ReadAll(r)
		if err != nil {
			return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidFile, "read: %v", err)
		}
		if err := ctx.Err(); err != nil {
			return model.CSVDocument{}, err
		}
		header = []string{name}
		rows = []model.Record{{RowNumber: 1, Fields: []string{strings.TrimSpace(string(content))}}}
	case TextLineRegex:
		if p.re == nil {
			return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidConfig, "text parser: line-regex not compiled")
		}
		names := p.re.SubexpNames()[1:]
		header = append([]string(nil), names...)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		var row int64
		for sc.Scan() {
			if err := ctx.Err(); err != nil {
				return model.CSVDocument{}, err
			}
			line := strings.TrimRight(sc.Text(), "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			m := p.re.FindStringSubmatch(line)
			if m == nil {
				if p.Strict {
					return model.CSVDocument{}, errs.Sourcef(errs.ErrMalformedCSV, "line %q does not match pattern", line)
				}
				continue
			}
			row++
			fields := make([]string, len(names))
			for i := range names {
				fields[i] = m[i+1]
			}
			rows = append(rows, model.Record{RowNumber: row, Fields: fields})
		}
		if err := sc.Err(); err != nil {
			return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidFile, "read: %v", err)
		}
	case TextKeyValue:
		sep := p.Separator
		if sep == "" {
			sep = "="
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		values := map[string]string{}
		var line int
		for sc.Scan() {
			if err := ctx.Err(); err != nil {
				return model.CSVDocument{}, err
			}
			line++
			lineText := strings.TrimSpace(strings.TrimRight(sc.Text(), "\r"))
			if lineText == "" || strings.HasPrefix(lineText, "#") {
				continue
			}
			k, v, ok := strings.Cut(lineText, sep)
			if !ok {
				return model.CSVDocument{}, errs.Sourcef(errs.ErrMalformedCSV, "line %d: %q is not key%value", line, lineText, sep)
			}
			k = strings.TrimSpace(k)
			if _, dup := values[k]; !dup {
				header = append(header, k)
			}
			values[k] = strings.TrimSpace(v)
		}
		if err := sc.Err(); err != nil {
			return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidFile, "read: %v", err)
		}
		sort.Strings(header) // deterministic column order for the snapshot
		fields := make([]string, len(header))
		for i, k := range header {
			fields[i] = values[k]
		}
		if len(header) > 0 {
			rows = []model.Record{{RowNumber: 1, Fields: fields}}
		}
	default:
		return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidConfig, "text parser: unknown format %q", p.Format)
	}
	stream := &memStream{header: header, headerSet: len(header) > 0, rows: rows, done: len(rows) == 0}
	return model.CSVDocument{Data: stream}, nil
}

// memStream serves pre-parsed records as a model.RecordStream.
type memStream struct {
	header    []string
	headerSet bool
	rows      []model.Record
	i         int
	done      bool
}

func (s *memStream) Header() []string {
	if s.headerSet {
		return append([]string(nil), s.header...)
	}
	return nil
}

func (s *memStream) Next() (model.Record, error) {
	if s.done || s.i >= len(s.rows) {
		return model.Record{}, model.ErrEOF
	}
	rec := s.rows[s.i]
	s.i++
	return rec, nil
}

// TextParserFromConfig builds a TextParser from component config keys:
// text_format, pattern, value_name, separator, strict.
func TextParserFromConfig(cc config.ComponentConfig) (*TextParser, error) {
	format := configutil.OptionalString(cc, "text_format", TextSingleValue)
	p, err := NewText(format)
	if err != nil {
		return nil, err
	}
	p.Pattern = configutil.OptionalString(cc, "pattern", "")
	p.ValueName = configutil.OptionalString(cc, "value_name", "value")
	p.Separator = configutil.OptionalString(cc, "separator", "=")
	p.Strict = configutil.OptionalBool(cc, "strict", false)
	if err := p.Compile(); err != nil {
		return nil, err
	}
	return p, nil
}

// NewTextParserFromConfigValues is the config-facing constructor used by the
// text-parser component (keys: text_format, pattern, value_name, separator,
// strict).
func NewTextParserFromConfigValues(cfg map[string]any) (*TextParser, error) {
	cc := config.ComponentConfig{Config: cfg}
	format := configutil.OptionalString(cc, "text_format", TextSingleValue)
	p, err := NewText(format)
	if err != nil {
		return nil, err
	}
	p.Pattern = configutil.OptionalString(cc, "pattern", "")
	p.ValueName = configutil.OptionalString(cc, "value_name", "value")
	p.Separator = configutil.OptionalString(cc, "separator", "=")
	p.Strict = configutil.OptionalBool(cc, "strict", false)
	if err := p.Compile(); err != nil {
		return nil, err
	}
	return p, nil
}
