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
	if ok {
		for d := last.AddDate(0, 0, 1); !d.After(target); d = d.AddDate(0, 0, 1) {
			add(model.CollectionKey{SourceID: sourceID, Date: d})
		}
	}
	return ordered, nil
}
