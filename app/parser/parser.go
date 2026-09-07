package parser

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// Parser is a streaming, standard-encoding/csv-backed parser. It owns no file
// handles and has no knowledge of collectors, dates, storage, or Runtime.
//
// Some exported CSV files begin with a few metadata lines (device, export
// time, comments) before the actual table. SkipLines drops that many leading
// physical lines so the table can start at a later line.
type Parser struct {
	Header    bool
	Comma     rune
	SkipLines int
}

func New() *Parser { return &Parser{} }

func (p *Parser) Parse(ctx context.Context, r io.Reader) (model.RecordStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return nil, errs.Sourcef(errs.ErrInvalidFile, "nil reader")
	}
	br := bufio.NewReader(r)
	if peek, err := br.Peek(3); err == nil && len(peek) == 3 && peek[0] == 0xEF && peek[1] == 0xBB && peek[2] == 0xBF {
		_, _ = br.Discard(3)
	}
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
