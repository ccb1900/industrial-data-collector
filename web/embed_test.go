package web

import (
	"io/fs"
	"testing"
)

func TestDistEmbeddedLayout(t *testing.T) {
	// go:embed all:dist stores files under a "dist/" prefix.
	if _, err := Dist.Open("dist/index.html"); err != nil {
		t.Fatalf("embedded dist/index.html: %v", err)
	}
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	// After fs.Sub the server can serve index.html at the root.
	if _, err := sub.Open("index.html"); err != nil {
		t.Fatalf("sub root index.html: %v", err)
	}
}
