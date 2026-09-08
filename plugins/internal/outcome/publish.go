// Package outcome publishes collection results as Application Runtime events.
// It is shared by the legacy csv-collector and per-Source source-unit
// Components so both write the same observable state without duplicating the
// event projection.
package outcome

import (
	"context"

	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
)

// Publish emits file and collection outcome events for one collection result.
// Handlers observe through the Application Query Adapter; this function never
// depends on an observer.
func Publish(ctx context.Context, emitCtx *runtime.Context, res *model.CollectionResult) {
	if emitCtx == nil || res == nil {
		return
	}
	for i := range res.Files {
		fr := &res.Files[i]
		switch fr.Status {
		case model.StatusSucceeded:
			_ = runtime.Serial(ctx, emitCtx, events.FileCompleted, events.FileCompletedPayload{
				Key: res.Key, File: fr.File, Metadata: fr.Metadata, Records: fr.Records,
			})
		case model.StatusFailed:
			_ = runtime.Serial(ctx, emitCtx, events.FileFailed, events.FileFailedPayload{
				Key: res.Key, File: fr.File, Metadata: fr.Metadata, Records: fr.Records, Error: fr.Error,
			})
		}
	}
	switch res.Status {
	case model.StatusSucceeded:
		_ = runtime.Serial(ctx, emitCtx, events.CollectionCompleted, events.CollectionCompletedPayload{
			Key: res.Key, Files: len(res.Files), Records: res.Records, Duration: res.Duration, EndedAt: res.EndedAt,
		})
	case model.StatusFailed:
		_ = runtime.Serial(ctx, emitCtx, events.CollectionFailed, events.CollectionFailedPayload{
			Key: res.Key, Error: res.Error, Duration: res.Duration,
		})
	}
}
