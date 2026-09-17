package parser

import (
	"context"
	"strings"
	"testing"
)

// 真实设备导出常见锯齿 CSV：各行字段数不一（尾随空列多少不定）。
// allow_ragged 放行变长行；关闭时保持 encoding/csv 严格模式。
func TestAllowRaggedRows(t *testing.T) {
	data := "a,b,c\n1,2,3\n4,5\n6,7,8,9\n"

	p := New()
	p.Header = true
	doc, err := p.Parse(context.Background(), strings.NewReader(data))
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	// 严格模式在惰性读取到锯齿行时报 malformed CSV（首行字段数正常，成功）。
	if _, err := doc.Data.Next(); err != nil {
		t.Fatalf("first regular row: %v", err)
	}
	if _, err := doc.Data.Next(); err == nil {
		t.Fatal("strict mode must reject ragged rows")
	}

	p = New()
	p.Header = true
	p.AllowRagged = true
	doc, err = p.Parse(context.Background(), strings.NewReader(data))
	if err != nil {
		t.Fatalf("ragged parse: %v", err)
	}
	if got := strings.Join(doc.Data.Header(), ","); got != "a,b,c" {
		t.Fatalf("header = %q", got)
	}
	var rows [][]string
	for {
		rec, err := doc.Data.Next()
		if err != nil {
			break
		}
		rows = append(rows, rec.Fields)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if len(rows[1]) != 2 || rows[1][1] != "5" {
		t.Fatalf("short row = %v", rows[1])
	}
	if len(rows[2]) != 4 || rows[2][3] != "9" {
		t.Fatalf("long row = %v", rows[2])
	}
}
