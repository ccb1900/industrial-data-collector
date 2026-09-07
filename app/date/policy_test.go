package date

import (
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func TestYesterdayPolicy(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	p := Policy{Type: PolicyYesterday, Now: func() time.Time { return now }}
	d, err := p.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if d.String() != "2026-09-06" {
		t.Fatalf("yesterday = %s", d)
	}
}

func TestSpecificPolicy(t *testing.T) {
	p := Policy{Type: PolicySpecific, Specific: NewDate(t, "2026-09-03")}
	d, err := p.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if d.String() != "2026-09-03" {
		t.Fatalf("specific = %s", d)
	}
}

func TestUnknownPolicyRejected(t *testing.T) {
	p := Policy{Type: "range"}
	if _, err := p.Resolve(); err == nil {
		t.Fatal("unknown policy must error")
	}
}

func NewDate(t *testing.T, s string) model.CollectionDate {
	t.Helper()
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}
