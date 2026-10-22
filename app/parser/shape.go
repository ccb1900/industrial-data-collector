package parser

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"

	appencoding "gocordis-csv-collector/app/encoding"
	"gocordis-csv-collector/app/errs"
)

// Shape is the row/column matrix of one CSV stream, measured without
// materialising records. The consistency check treats it as the truth of the
// file: the sink has to reproduce exactly these rows and columns.
type Shape struct {
	// Rows is the number of data records (the header is not a row).
	Rows int64
	// Cols is the header width, or the widest data row when the file has no
	// header line.
	Cols int
	// Ragged counts data rows whose field count differs from Cols.
	Ragged int64
	// Header is the decoded header line (nil when Header is false).
	Header []string
}

// Structured reports whether the parser runs in explicit metadata-section mode.
// That mode's row semantics belong to the document reader, not to the flat
// scanner, so the consistency check treats such sources as not measurable.
func (p *Parser) Structured() bool { return p.Document.Enabled() }

// RaggedAllowed reports whether width-varying rows are a configured fact of
// this source rather than a defect.
func (p *Parser) RaggedAllowed() bool { return p.AllowRagged }

// Scan measures the file behind r with the parser's own decode, preamble
// (skip_lines) and header semantics, but always with ragged rows allowed: the
// scanner reports width variation instead of failing on it. It reads the whole
// stream, so the caller owns r and must close it.
func (p *Parser) Scan(ctx context.Context, r io.Reader) (Shape, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return Shape{}, errs.Sourcef(errs.ErrInvalidFile, "nil reader")
	}
	name, err := appencoding.Normalize(p.Encoding)
	if err != nil {
		return Shape{}, errs.Sourcef(errs.ErrInvalidConfig, "%v", err)
	}
	br := bufio.NewReader(r)
	src, err := p.decodeStream(br, name)
	if err != nil {
		return Shape{}, err
	}
	return p.scanFlat(ctx, bufio.NewReader(src))
}

func (p *Parser) scanFlat(ctx context.Context, br *bufio.Reader) (Shape, error) {
	out := Shape{}
	widths := map[int]int64{}
	if p.SkipLines < 0 {
		return out, errs.Sourcef(errs.ErrInvalidFile, "skip_lines must be >= 0")
	}
	for i := 0; i < p.SkipLines; i++ {
		if _, err := br.ReadString('\n'); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, errs.Sourcef(errs.ErrMalformedCSV, "csv preamble line %d: %v", i+1, err)
		}
	}
	cr := csv.NewReader(br)
	if p.Comma != 0 {
		cr.Comma = p.Comma
	}
	// Always measure: FieldsPerRecord = -1 makes encoding/csv hand back every
	// row whatever its width, so the scanner can count the ragged ones.
	cr.FieldsPerRecord = -1
	if p.Header {
		fields, err := cr.Read()
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, classifyCSVError(fields, err)
		}
		out.Header = append([]string(nil), fields...)
		out.Cols = len(fields)
	}
	seen := int64(0)
	for {
		fields, err := cr.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return out, classifyCSVError(fields, err)
		}
		seen++
		out.Rows++
		widths[len(fields)]++
		if seen%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return out, fmt.Errorf("%w: %v", errs.ErrTimeout, err)
			}
		}
	}
	cols := out.Cols
	if cols == 0 {
		// Headerless file: the widest row is the matrix width.
		for w, n := range widths {
			if n > 0 && w > cols {
				cols = w
			}
		}
		out.Cols = cols
	}
	out.Ragged = out.Rows - widths[cols]
	return out, nil
}
