package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocordis-csv-collector/app/host"
	"gocordis-csv-collector/app/sourcecomp"
	sourceunitplugin "gocordis-csv-collector/plugins/sourceunit"
)

// The watch scenarios prove change-triggered collection: a plain-text file
// (not CSV, no date directory) fully rewritten by measurement software is
// collected automatically after the content stabilizes, identical rewrites
// are deduplicated by content hash, and the whole story is ordinary
// components — trigger, source, parser — emitting the same Runtime events.

func watchDocument(file string, stateDir string) string {
	return fmt.Sprintf(`
[profiles.gauge_csv]
parser = "text"
text_format = "key-value"
separator = "="
layout = "flat"
dedupe_content_hash = true
file_stable_window_seconds = 0
date_policy = "today"
collection_mode = "append"
batch_size = 1000

[profiles.memory_sink]
sink = "memory-storage"

[profiles.file_state]
state_type = "file-state"
state_dir = %q

[[sources]]
id = "gauge"
path = %q
profiles = ["gauge_csv", "memory_sink", "file_state"]

[[components]]
id = "watch"
type = "watch-file-trigger"

[components.config]
path = %q
source = "gauge"
debounce = "200ms"

[[components]]
id = "console-bridge"
type = "console-bridge"

[[components]]
id = "query-provider"
type = "query-provider"

[[components]]
id = "ui"
type = "ui"
`, stateDir, file, file)
}

func watchUnit(h *host.Host, id string) *sourceunitplugin.SourceUnitComponent {
	for _, o := range h.Owned() {
		if o.ID != sourcecomp.SourceComponentPrefix+id {
			continue
		}
		if u, ok := o.Fiber.Component().(*sourceunitplugin.SourceUnitComponent); ok {
			return u
		}
	}
	return nil
}

func TestWatchTriggeredCollection(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "x.txt")
	stateDir := filepath.Join(base, "state")

	v1 := "product=widget\nreading=42.5\n"

	parsed, err := sourcecomp.Parse([]byte(watchDocument(file, stateDir)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, parsed.Config)

	u := watchUnit(h, "gauge")
	if u == nil || u.MemoryStore() == nil {
		t.Fatal("gauge source unit not active")
	}

	// The "measurement software" writes the first snapshot: the watcher must
	// fire after the debounce window and collect it — no scheduler, no manual
	// trigger.
	if err := os.WriteFile(file, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "initial snapshot collected", func() bool {
		return u.MemoryStore().Total() == 1
	})

	// The software fully rewrites the file with new content: the new
	// snapshot is recorded as a new version.
	v2 := "product=widget\nreading=43.1\nstation=03\n"
	if err := os.WriteFile(file, []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "changed snapshot collected", func() bool {
		return u.MemoryStore().Total() == 2
	})

	// An identical rewrite (same bytes) carries no new information: the
	// content-hash dedup must not record it again.
	if err := os.WriteFile(file, []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // longer than the debounce window
	if got := u.MemoryStore().Total(); got != 2 {
		t.Fatalf("rows after identical rewrite = %d, want 2 (content dedup)", got)
	}

	// The snapshot record carries the parsed key columns as the header.
	batches := u.MemoryStore().Batches()
	if len(batches) == 0 || len(batches[len(batches)-1].Header) != 3 {
		t.Fatalf("last batch header = %#v, want the parsed key columns", batches[len(batches)-1].Header)
	}
}
