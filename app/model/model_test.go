package model

import (
	"testing"
	"time"
)

func TestCollectionDateIgnoresTimeZoneForCalendarComparison(t *testing.T) {
	sh := time.FixedZone("CST", 8*3600)
	utc := time.Date(2026, 9, 7, 16, 0, 0, 0, time.UTC)
	loc := time.Date(2026, 9, 7, 0, 0, 0, 0, sh)
	a, b := NewCollectionDate(utc), NewCollectionDate(loc)
	if !a.Equal(b) {
		t.Fatalf("calendar dates should be equal across zones: %s vs %s", a, b)
	}
	if !NewCollectionDate(utc.AddDate(0, 0, -1)).Before(a) {
		t.Fatal("calendar ordering failed")
	}
}

func TestCollectionKeyAndFileIdentityStable(t *testing.T) {
	k := CollectionKey{SourceID: "prod", Date: NewCollectionDate(time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC))}
	want := "prod/2026-09-06"
	if k.String() != want {
		t.Fatalf("key = %q, want %q", k.String(), want)
	}
	f1 := FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 10, ModTime: time.Unix(10, 0)}
	f2 := f1
	if f1.Identity() != f2.Identity() {
		t.Fatal("same identity must be equal")
	}
	f2.Size++
	if f1.Identity() == f2.Identity() {
		t.Fatal("size change must change identity")
	}
}

func TestM11MetadataNeverParticipatesInFileIdentity(t *testing.T) {
	f := FileIdentity{SourceID: "prod", Path: "/a.csv", Name: "a.csv", Size: 10, ModTime: time.Unix(10, 0)}
	base := f.Identity()
	if base == "" {
		t.Fatal("identity must be non-empty")
	}
	// A metadata change (v1 -> v2) must yield the same file identity even when
	// metadata travels next to the file (FileDescriptor / Batch / FileResult).
	v1 := Metadata{Values: map[string]string{"line": "line-A"}}
	v2 := Metadata{Values: map[string]string{"line": "line-B", "station": "ST01"}}
	if v1.Equal(v2) {
		t.Fatal("test fixtures must differ")
	}
	for _, d := range []FileDescriptor{
		{Identity: f, Metadata: v1},
		{Identity: f, Metadata: v2},
	} {
		if d.Identity.Identity() != base {
			t.Fatal("metadata must never participate in FileIdentity.Identity()")
		}
	}
}
