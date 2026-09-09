package recovery

import (
	"context"
	"fmt"
	"time"

	"gocordis-csv-collector/app/model"
)

// Planner builds the set of collection keys a trigger should attempt: known
// incomplete rows plus calendar gaps since the last completed business date.
type Planner struct {
	State model.CollectionState
	Now   func() time.Time
	// CatchupDays bounds the synthesized calendar gap when the state chain has
	// no succeeded business date inside the window (first deployment, deleted
	// state, or a source that never succeeded). Zero synthesizes gaps only
	// from the last succeeded date, as before. Known incomplete rows are
	// always attempted regardless of this window: they are recorded evidence,
	// not synthesized guesses.
	CatchupDays int
}

func (p *Planner) Plan(ctx context.Context, sourceID model.SourceID, target model.CollectionDate) ([]model.CollectionKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	// Never recover dates after today when a caller asks for "today"; this is
	// defensive only because Recovery is only triggered by an application
	// request.
	_ = now

	known, err := p.State.ListIncomplete(ctx, sourceID, target, 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("list incomplete: %w", err)
	}
	byKey := make(map[model.CollectionKey]struct{}, len(known)+1)
	var ordered []model.CollectionKey
	add := func(k model.CollectionKey) {
		if _, ok := byKey[k]; ok {
			return
		}
		byKey[k] = struct{}{}
		ordered = append(ordered, k)
	}
	for _, k := range known {
		if !k.Date.After(target) {
			add(k)
		}
	}
	last, ok, err := p.State.LastCompleted(ctx, sourceID, target)
	if err != nil {
		return nil, fmt.Errorf("last completed: %w", err)
	}
	// Synthesize the gap (last success, target]. With CatchupDays > 0 the
	// window start is clamped to at most CatchupDays calendar days before the
	// target, so a first deployment or a lost state cannot reach back
	// indefinitely.
	start := target
	planGap := false
	if ok {
		start = last.AddDate(0, 0, 1)
		planGap = !start.After(target)
	}
	if p.CatchupDays > 0 {
		earliest := target.AddDate(0, 0, -(p.CatchupDays - 1))
		if !ok || start.Before(earliest) {
			start = earliest
		}
		planGap = !start.After(target)
	}
	if planGap {
		for d := start; !d.After(target); d = d.AddDate(0, 0, 1) {
			add(model.CollectionKey{SourceID: sourceID, Date: d})
		}
	}
	return ordered, nil
}
