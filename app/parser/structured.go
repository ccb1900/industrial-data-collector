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

// parseStructuredDocument parses the explicit Metadata Section declared by
// Parser.Document. Physical rows are read before the Data Section so blank
// separator rows are never mistaken for data. The remaining Data Section is
// then streamed through encoding/csv with the parsed header fixed.
func (p *Parser) parseStructuredDocument(ctx context.Context, br *bufio.Reader) (model.CSVDocument, error) {
	if !p.Header {
		return model.CSVDocument{}, fmt.Errorf("%w: structured csv.metadata mode requires header=true", errs.ErrInvalidConfig)
	}
	if p.SkipLines > 0 {
		return model.CSVDocument{}, fmt.Errorf("%w: structured csv.metadata mode cannot be combined with skip_lines", errs.ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return model.CSVDocument{}, err
	}
	doc := model.CSVDocument{Metadata: model.NewMetadata(), Structured: true}
	cfg := p.Document
	lineNo := 1

	for ; lineNo < cfg.StartRow; lineNo++ {
		line, err := readPhysicalLine(br)
		if err != nil {
			return doc, fmt.Errorf("%w: data header not found (row %d)", errs.ErrMalformedCSV, cfg.HeaderRow)
		}
		if !isBlankCSVLine(line) {
			return doc, fmt.Errorf("%w: csv row %d must be blank before metadata start_row", errs.ErrMalformedCSV, lineNo)
		}
	}

	seen := make(map[string]int)
	for ; lineNo <= cfg.EndRow; lineNo++ {
		line, err := readPhysicalLine(br)
		if err != nil {
			return doc, fmt.Errorf("%w: csv metadata row %d: unexpected end of file", errs.ErrMalformedCSV, lineNo)
		}
		fields, err := parseCSVLine(line, lineNo)
		if err != nil {
			return doc, err
		}
		if fields == nil {
			return doc, fmt.Errorf("%w: csv metadata row %d must contain key,value", errs.ErrMalformedCSV, lineNo)
		}
		if len(fields) != 2 {
			return doc, fmt.Errorf("%w: csv metadata row %d is malformed: want key,value (got %d fields)", errs.ErrMalformedCSV, lineNo, len(fields))
		}
		for _, f := range fields {
			if !utf8.ValidString(f) {
				return doc, errs.Sourcef(errs.ErrInvalidEncoding, "csv metadata row %d contains invalid UTF-8", lineNo)
			}
		}
		key := strings.TrimSpace(fields[0])
		if key == "" {
			return doc, fmt.Errorf("%w: csv metadata row %d: empty metadata key", errs.ErrMalformedCSV, lineNo)
		}
		if prev, dup := seen[key]; dup {
			return doc, fmt.Errorf("%w: csv metadata row %d: duplicate metadata key: %s (first row %d)", errs.ErrMalformedCSV, lineNo, key, prev)
		}
		seen[key] = lineNo
		doc.Metadata.Values[key] = fields[1]
	}

	for ; lineNo < cfg.HeaderRow; lineNo++ {
		line, err := readPhysicalLine(br)
		if err != nil {
			return doc, fmt.Errorf("%w: data header not found (expected row %d)", errs.ErrMalformedCSV, cfg.HeaderRow)
		}
		if !isBlankCSVLine(line) {
			return doc, fmt.Errorf("%w: csv row %d must be a blank separator before data header", errs.ErrMalformedCSV, lineNo)
		}
	}

	headerLine, err := readPhysicalLine(br)
	if err != nil {
		return doc, fmt.Errorf("%w: data header not found (expected row %d)", errs.ErrMalformedCSV, cfg.HeaderRow)
	}
	header, err := parseCSVLine(headerLine, cfg.HeaderRow)
	if err != nil {
		return doc, err
	}
	if header == nil || len(header) == 0 {
		return doc, fmt.Errorf("%w: csv data header row %d is empty", errs.ErrMalformedCSV, cfg.HeaderRow)
	}
	seenHeader := make(map[string]int, len(header))
	for i, col := range header {
		if !utf8.ValidString(col) {
			return doc, errs.Sourcef(errs.ErrInvalidEncoding, "csv data header row %d contains invalid UTF-8", cfg.HeaderRow)
		}
		if prev, dup := seenHeader[col]; dup {
			return doc, fmt.Errorf("%w: duplicate data header %q at row %d column %d (first column %d)", errs.ErrMalformedCSV, col, cfg.HeaderRow, i+1, prev+1)
		}
		seenHeader[col] = i
	}

	cr := csv.NewReader(br)
	if p.Comma != 0 {
		cr.Comma = p.Comma
	}
	cr.FieldsPerRecord = len(header)
	doc.Data = &stream{
		ctx:       ctx,
		parser:    p,
		csv:       cr,
		headerSet: true,
		header:    append([]string(nil), header...),
	}
	return doc, nil
}

func readPhysicalLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if len(line) == 0 && err != nil {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

func isBlankCSVLine(line string) bool {
	return strings.TrimSpace(line) == ""
}

// parseCSVLine parses one physical CSV line with encoding/csv. Blank lines
// return nil,nil. Errors carry the physical row number.
func parseCSVLine(line string, lineNo int) ([]string, error) {
	if isBlankCSVLine(line) {
		return nil, nil
	}
	fields, err := csv.NewReader(strings.NewReader(line)).Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, errs.Sourcef(errs.ErrMalformedCSV, "csv row %d: %v", lineNo, err)
	}
	return fields, nil
}
