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

// CollectionKey is used as a read model map key, so struct equality — not the
// zone-tolerant Equal — is the comparison that matters. A key built from
// local-time policy resolution and a key parsed from a DTO "YYYY-MM-DD" must
// hit the same entry (regression: ListFiles missed under TZ != UTC).
func TestCollectionKeyEqualAcrossZonesAsMapKey(t *testing.T) {
	sh := time.FixedZone("CST", 8*3600)
	fromPolicy := CollectionKey{
		SourceID: "prod",
		Date:     NewCollectionDate(time.Date(2026, 9, 8, 1, 0, 0, 0, sh)),
	}
	var fromDTO CollectionKey
	if err := fromDTO.Date.UnmarshalText([]byte("2026-09-08")); err != nil {
		t.Fatal(err)
	}
	fromDTO.SourceID = "prod"
	if fromPolicy != fromDTO {
		t.Fatalf("keys must be equal as map keys: %s vs %s", fromPolicy.Date.Time(), fromDTO.Date.Time())
	}
	m := map[CollectionKey]string{fromPolicy: "hit"}
	if m[fromDTO] != "hit" {
		t.Fatal("map lookup across zone representations failed")
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
