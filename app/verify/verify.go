// Package verify checks a collection's stored result against the file it came
// from. The file is the only source of truth: its row count, its header width
// and the rows the pipeline recorded must all equal what the sink holds. A
// mismatch is never repaired automatically — it is evidence for an operator.
package verify

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/parser"
)

// Shaper measures a file's row/column matrix. *parser.Parser implements it.
type Shaper interface {
	Scan(ctx context.Context, r io.Reader) (parser.Shape, error)
	Structured() bool
	RaggedAllowed() bool
}

// Sink is the storage side of a check: the rows it holds for one collection,
// and how a file header maps onto the columns it stores.
type Sink interface {
	CountFileRows(ctx context.Context, key model.CollectionKey) (map[string]int64, error)
	ColumnCoverage(header []string) model.ColumnCoverage
}

// Opener hands back one collected file for re-measurement. The implementation
// decides which copy is authoritative (the local backup when present, the
// source file otherwise) and reports a changed file as an error.
type Opener interface {
	Open(ctx context.Context, key model.CollectionKey, file model.FileIdentity) (io.ReadCloser, error)
}

// Checker runs one collection's checks. A nil Sink or a nil Opener degrades to
// Skipped/Error rows rather than failing the caller.
type Checker struct {
	Shaper Shaper
	Sink   Sink
	Opener Opener
	Now    func() time.Time
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// CheckCollection re-measures the completed files of one collection and
// compares them against the sink. Measurement and count problems become Error
// rows; they never change the collection's own outcome.
func (c *Checker) CheckCollection(ctx context.Context, key model.CollectionKey, files []model.FileRecordView) []model.FileCheck {
	out := make([]model.FileCheck, 0, len(files))
	if c.Shaper == nil {
		for _, f := range files {
			out = append(out, c.skipped(key, f, "未配置文件扫描器"))
		}
		return out
	}
	if c.Shaper.Structured() {
		for _, f := range files {
			out = append(out, c.skipped(key, f, "结构化 csv.metadata 布局无法按行测量"))
		}
		return out
	}
	var counts map[string]int64
	var countErr error
	if c.Sink != nil {
		counts, countErr = c.Sink.CountFileRows(ctx, key)
	}
	for _, f := range files {
		out = append(out, c.checkOne(ctx, key, f, counts, countErr))
	}
	return out
}

func (c *Checker) skipped(key model.CollectionKey, f model.FileRecordView, note string) model.FileCheck {
	return model.FileCheck{
		Key: key, File: f.File, ParsedRows: f.Records,
		Status: model.CheckSkipped, Note: note, CheckedAt: c.now(),
	}
}

func (c *Checker) checkOne(ctx context.Context, key model.CollectionKey, f model.FileRecordView, counts map[string]int64, countErr error) model.FileCheck {
	ch := model.FileCheck{
		Key: key, File: f.File, ParsedRows: f.Records, CheckedAt: c.now(),
	}
	if c.Sink == nil {
		ch.Status = model.CheckSkipped
		ch.Note = "存储端未提供行数统计"
		return ch
	}
	if countErr != nil {
		ch.Status = model.CheckError
		ch.Note = "统计库中行数失败：" + countErr.Error()
		return ch
	}
	if c.Opener == nil {
		ch.Status = model.CheckSkipped
		ch.Note = "未配置文件读取器"
		return ch
	}
	rc, err := c.Opener.Open(ctx, key, f.File)
	if err != nil {
		ch.Status = model.CheckError
		ch.Note = "无法重新读取文件：" + err.Error()
		return ch
	}
	defer rc.Close()
	shape, err := c.Shaper.Scan(ctx, rc)
	if err != nil {
		ch.Status = model.CheckError
		ch.Note = "文件扫描失败：" + err.Error()
		return ch
	}
	ch.FileRows = shape.Rows
	ch.FileCols = shape.Cols
	ch.RaggedRows = shape.Ragged
	ch.DBRows = counts[f.File.Identity()]
	cov := c.Sink.ColumnCoverage(shape.Header)
	ch.StoredCols = cov.Stored
	ch.Unmapped = cov.Unmapped

	var hard, soft []string
	if shape.Rows != ch.DBRows {
		hard = append(hard, fmt.Sprintf("行数不符：文件 %d 行，库中 %d 行", shape.Rows, ch.DBRows))
	}
	if shape.Rows != ch.ParsedRows {
		hard = append(hard, fmt.Sprintf("行数不符：文件 %d 行，台账 %d 行", shape.Rows, ch.ParsedRows))
	}
	if shape.Ragged > 0 {
		msg := fmt.Sprintf("残缺行 %d 行（共 %d 行）", shape.Ragged, shape.Rows)
		if c.Shaper.RaggedAllowed() {
			soft = append(soft, msg)
		} else {
			hard = append(hard, msg)
		}
	}
	if len(cov.Unmapped) > 0 {
		msg := fmt.Sprintf("列数不符：文件 %d 列，入库 %d 列（未入库：%s）", shape.Cols, cov.Stored, strings.Join(cov.Unmapped, "、"))
		if cov.LossAllowed {
			soft = append(soft, msg)
		} else {
			hard = append(hard, msg)
		}
	}
	switch {
	case len(hard) > 0:
		ch.Status = model.CheckMismatch
		ch.Note = strings.Join(hard, "；")
	case len(soft) > 0:
		ch.Status = model.CheckWarning
		ch.Note = strings.Join(soft, "；")
	default:
		ch.Status = model.CheckOK
	}
	return ch
}

// Summary counts a check batch by status.
type Summary struct {
	Checked  int
	OK       int
	Mismatch int
	Warning  int
	Error    int
	Skipped  int
}

func Summarize(checks []model.FileCheck) Summary {
	var s Summary
	for _, c := range checks {
		s.Checked++
		switch c.Status {
		case model.CheckOK:
			s.OK++
		case model.CheckMismatch:
			s.Mismatch++
		case model.CheckWarning:
			s.Warning++
		case model.CheckError:
			s.Error++
		default:
			s.Skipped++
		}
	}
	return s
}
