package tests

import (
	"context"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

// CSV-E2E-11: the scheduler component is the emitter; the collector owns the
// CollectionRequested handler registered as a Runtime Effect.
func TestCSVE2E11SchedulerEventCollector(t *testing.T) {
	root := t.TempDir()
	if err := writeDay(root, "2026-09-06", "a.csv", "id,name\n1,scheduled\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newApp(t)
	defer h.Close(context.Background())
	active(ctx, t, h, cfg(basicComponents(root, "src", "local-file-source", "store", "", "specific", "2026-09-06")...))
	// Trigger is the scheduler capability entry point, which dispatches through
	// the typed Runtime Event key rather than calling Collector directly.
	if err := h.Trigger(ctx, model.CollectionRequested{Reason: "scheduled", Date: ptrD(cfgDate(t, "2026-09-06"))}); err != nil {
		t.Fatal(err)
	}
	if got := rows(h, "store"); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}
