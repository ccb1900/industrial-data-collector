package parser

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gocordis-csv-collector/app/model"
)

// countingReader 记录底层真实读到的字节数。它是"真流式"的裁判：
// 行正则模式此前把整个文件读进内存再吐记录，对 2GB 的设备日志等于把
// 批次缓冲的意义全部抵消。
type countingReader struct {
	r    *strings.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.read += int64(k)
	return k, err
}

func lineRegexBody(lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "2026-11-19 12:00:%02d temp=25.%02d\n", i%60, i%100)
	}
	return b.String()
}

// TestTextLineRegexStreamsInsteadOfBuffering 是本文件的红线：取前 10 条记录时，
// 底层读到的字节必须远小于文件总量。改前 Parse 在返回任何记录之前
// io.ReadAll/append 全部行——20 万行的文件在第一条记录离开解析器之前就
// 全量驻留了。
func TestTextLineRegexStreamsInsteadOfBuffering(t *testing.T) {
	const lines = 200_000
	body := lineRegexBody(lines)
	cr := &countingReader{r: strings.NewReader(body)}

	p, err := NewText(TextLineRegex)
	if err != nil {
		t.Fatal(err)
	}
	p.Pattern = `(?<ts>\S+ \S+) temp=(?<t>\S+)`
	if err := p.Compile(); err != nil {
		t.Fatal(err)
	}

	doc, err := p.Parse(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	stream := doc.Data
	if got := stream.Header(); len(got) != 2 || got[0] != "ts" || got[1] != "t" {
		t.Fatalf("header = %v, want [ts t]", got)
	}
	for i := 0; i < 10; i++ {
		rec, err := stream.Next()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if rec.RowNumber != int64(i+1) {
			t.Fatalf("row number = %d, want %d", rec.RowNumber, i+1)
		}
	}
	if cr.read > int64(len(body))/2 {
		t.Errorf("taking 10 records already read %d of %d bytes — 解析器在全量缓冲，不是流式",
			cr.read, len(body))
	}
	// 剩余记录也要能顺利读完，EOF 语义不变。
	var n int
	for {
		_, err := stream.Next()
		if err == model.ErrEOF {
			break
		}
		if err != nil {
			t.Fatalf("stream error at %d: %v", n, err)
		}
		n++
	}
	if n != lines-10 {
		t.Fatalf("total records = %d, want %d", n+10, lines)
	}
}

// TestTextLineRegexStreamMatchesBufferedSemantics 钉住行为不变：空行跳过、
// 非匹配行（非严格）跳过、行号只对产出记录计数。
func TestTextLineRegexStreamMatchesBufferedSemantics(t *testing.T) {
	body := "2026-11-19 12:00:01 temp=25.5\n" +
		"\n" +
		"header noise line\n" +
		"2026-11-19 12:00:02 temp=26.0\n" +
		"   \n" +
		"2026-11-19 12:00:03 temp=24.9\n"
	p, err := NewText(TextLineRegex)
	if err != nil {
		t.Fatal(err)
	}
	p.Pattern = `(?<ts>\S+ \S+) temp=(?<t>\S+)`
	if err := p.Compile(); err != nil {
		t.Fatal(err)
	}
	doc, err := p.Parse(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		row int64
		ts  string
		val string
	}{
		{1, "2026-11-19 12:00:01", "25.5"},
		{2, "2026-11-19 12:00:02", "26.0"},
		{3, "2026-11-19 12:00:03", "24.9"},
	}
	for _, w := range want {
		rec, err := doc.Data.Next()
		if err != nil {
			t.Fatalf("record %d: %v", w.row, err)
		}
		if rec.RowNumber != w.row || rec.Fields[0] != w.ts || rec.Fields[1] != w.val {
			t.Fatalf("record = %+v, want row %d %s %s", rec, w.row, w.ts, w.val)
		}
	}
	if _, err := doc.Data.Next(); err != model.ErrEOF {
		t.Fatalf("after last record: err=%v, want EOF", err)
	}
}

// TestTextLineRegexStreamStrictFailsMidStream：严格模式下非匹配行必须让
// Next 报错（采集器据此判整个文件失败），错误里带行号。
func TestTextLineRegexStreamStrictFailsMidStream(t *testing.T) {
	body := "2026-11-19 12:00:01 temp=25.5\nbad line\n2026-11-19 12:00:02 temp=26.0\n"
	p, err := NewText(TextLineRegex)
	if err != nil {
		t.Fatal(err)
	}
	p.Pattern = `(?<ts>\S+ \S+) temp=(?<t>\S+)`
	p.Strict = true
	if err := p.Compile(); err != nil {
		t.Fatal(err)
	}
	doc, err := p.Parse(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Data.Next(); err != nil {
		t.Fatalf("first record: %v", err)
	}
	_, err = doc.Data.Next()
	if err == nil || err == model.ErrEOF {
		t.Fatalf("strict mismatch must fail the stream, got %v", err)
	}
	// 错误携带行内容（不是行号）：现场拿着报错去文件里搜原文即可定位。
	if !strings.Contains(err.Error(), "bad line") {
		t.Errorf("strict error must quote the offending line, got %v", err)
	}
}
