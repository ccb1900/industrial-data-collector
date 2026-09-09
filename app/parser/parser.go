package parser

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	appencoding "gocordis-csv-collector/app/encoding"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"golang.org/x/text/transform"
)

// Parser is a streaming, standard-encoding/csv-backed parser. It owns no file
// handles and has no knowledge of collectors, dates, storage, or Runtime.
//
// Some exported CSV files begin with a few metadata lines (device, export
// time, comments) before the actual table. SkipLines drops that many leading
// physical lines so the table can start at a later line.
//
// Encoding selects the character encoding of the stream (utf8 default; gbk,
// gb18030, big5, latin1, windows1252, utf16le/be, auto — see app/encoding).
// A byte-order mark always wins over the configured value: it is stripped
// for UTF-8 and selects the byte order for UTF-16.
type Parser struct {
	Header    bool
	Comma     rune
	SkipLines int
	Document  DocumentConfig
	Encoding  string
}

func New() *Parser { return &Parser{} }

func (p *Parser) Parse(ctx context.Context, r io.Reader) (model.CSVDocument, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidFile, "nil reader")
	}
	name, err := appencoding.Normalize(p.Encoding)
	if err != nil {
		return model.CSVDocument{}, errs.Sourcef(errs.ErrInvalidConfig, "%v", err)
	}
	br := bufio.NewReader(r)
	src, err := p.decodeStream(br, name)
	if err != nil {
		return model.CSVDocument{}, err
	}
	if p.Document.Enabled() {
		return p.parseStructuredDocument(ctx, bufio.NewReader(src))
	}
	stream, err := p.parseFlat(ctx, bufio.NewReader(src))
	return model.CSVDocument{Data: stream}, err
}

// decodeStream resolves the byte-order mark and the configured encoding into
// a UTF-8 reader. UTF-8 passes through unchanged (strict validation stays
// downstream); every other encoding is decoded streaming.
func (p *Parser) decodeStream(br *bufio.Reader, name string) (io.Reader, error) {
	bom := appencoding.DetectBOM(mustPeek(br))
	switch bom {
	case appencoding.BOMUTF8:
		_, _ = br.Discard(3)
		return br, nil
	case appencoding.BOMUTF16LE, appencoding.BOMUTF16BE:
		_, _ = br.Discard(2)
		dec, err := appencoding.Decoder(bom.UTF16())
		if err != nil {
			return nil, errs.Sourcef(errs.ErrInvalidEncoding, "%v", err)
		}
		return transformReader(br, dec), nil
	default:
	}
	if name == appencoding.Auto {
		name = appencoding.ResolveAuto(mustPeek(br))
	}
	dec, err := appencoding.Decoder(name)
	if err != nil {
		return nil, errs.Sourcef(errs.ErrInvalidConfig, "%v", err)
	}
	if dec == nil {
		return br, nil
	}
	return transformReader(br, dec), nil
}

func mustPeek(br *bufio.Reader) []byte {
	head, _ := br.Peek(4)
	return head
}

// transformReader wraps a streaming decoder in its own buffer so the flat
// and structured readers keep their bufio contract.
func transformReader(br *bufio.Reader, dec transform.Transformer) *bufio.Reader {
	return bufio.NewReader(transform.NewReader(br, dec))
}

func (p *Parser) parseFlat(ctx context.Context, br *bufio.Reader) (*stream, error) {
	if p.SkipLines < 0 {
		return nil, errs.Sourcef(errs.ErrInvalidFile, "skip_lines must be >= 0")
	}
	for i := 0; i < p.SkipLines; i++ {
		if _, err := br.ReadString('\n'); err != nil {
			if err == io.EOF {
				// The preamble consumed the whole file: there is no table.
				s := &stream{ctx: ctx, parser: p, done: true}
				return s, nil
			}
			return nil, errs.Sourcef(errs.ErrMalformedCSV, "csv preamble line %d: %v", i+1, err)
		}
	}
	cr := csv.NewReader(br)
	if p.Comma != 0 {
		cr.Comma = p.Comma
	}
	s := &stream{ctx: ctx, parser: p, csv: cr}
	if p.Header {
		fields, err := cr.Read()
		if err != nil {
			if err == model.ErrEOF {
				s.done = true
				return s, nil
			}
			return nil, classifyCSVError(fields, err)
		}
		for _, f := range fields {
			if !utf8.ValidString(f) {
				return nil, errs.Sourcef(errs.ErrInvalidEncoding, "header contains invalid UTF-8")
			}
		}
		s.headerSet = true
		s.header = append([]string(nil), fields...)
	}
	return s, nil
}

type stream struct {
	ctx    context.Context
	parser *Parser
	csv    *csv.Reader

	header    []string
	headerSet bool
	rowNumber int64
	done      bool
}

func (s *stream) Header() []string {
	if s.headerSet {
		return append([]string(nil), s.header...)
	}
	return nil
}

func (s *stream) Next() (model.Record, error) {
	if s.done {
		return model.Record{}, model.ErrEOF
	}
	if err := s.ctx.Err(); err != nil {
		return model.Record{}, fmt.Errorf("%w: %v", errs.ErrTimeout, err)
	}
	fields, err := s.csv.Read()
	if err != nil {
		if err == io.EOF {
			s.done = true
			return model.Record{}, model.ErrEOF
		}
		return model.Record{}, classifyCSVError(fields, err)
	}
	for _, f := range fields {
		if !utf8.ValidString(f) {
			return model.Record{}, errs.Sourcef(errs.ErrInvalidEncoding, "row contains invalid UTF-8")
		}
	}
	s.rowNumber++
	return model.Record{
		RowNumber: s.rowNumber,
		Fields:    append([]string(nil), fields...),
	}, nil
}

func classifyCSVError(fields []string, err error) error {
	if isUTF8Related(fields) {
		return errs.Sourcef(errs.ErrInvalidEncoding, "csv field: %v", err)
	}
	return errs.Sourcef(errs.ErrMalformedCSV, "csv row: %v", err)
}

func isUTF8Related(fields []string) bool {
	for _, f := range fields {
		if !utf8.ValidString(f) {
			return true
		}
	}
	return strings.Contains(fmt.Sprint(fields), "\x00")
}
