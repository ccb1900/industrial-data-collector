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
