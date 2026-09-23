// Package outcome publishes collection results as Application Runtime events.
// It is shared by the legacy csv-collector and per-Source source-unit
// Components so both write the same observable state without duplicating the
// event projection.
package outcome

import (
	"context"
	"time"

	"dynamic-runtime/extensions/event"
	"dynamic-runtime/runtime"

	"gocordis-csv-collector/app/events"
	"gocordis-csv-collector/app/model"
)

// PublishCollectionStarted 在采集真正开始（台账 Begin 成功）的时点发布。
func PublishCollectionStarted(ctx context.Context, emitCtx *runtime.Context, key model.CollectionKey) {
	if emitCtx == nil {
		return
	}
	_ = event.Serial(ctx, emitCtx, events.CollectionStarted, events.CollectionStartedPayload{
		Key: key, StartedAt: time.Now(),
	})
}

// PublishFileStarted 在单文件开始处理的时点发布（已完成的短路文件不发）。
func PublishFileStarted(ctx context.Context, emitCtx *runtime.Context, key model.CollectionKey, file model.FileIdentity) {
	if emitCtx == nil {
		return
	}
	_ = event.Serial(ctx, emitCtx, events.FileStarted, events.FileStartedPayload{
		Key: key, File: file,
	})
}

// PublishFileResult 发布单文件的终结事件（Succeeded/Failed）。
func PublishFileResult(ctx context.Context, emitCtx *runtime.Context, key model.CollectionKey, fr *model.FileResult) {
	if emitCtx == nil {
		return
	}
	switch fr.Status {
	case model.StatusSucceeded:
		_ = event.Serial(ctx, emitCtx, events.FileCompleted, events.FileCompletedPayload{
			Key: key, File: fr.File, Metadata: fr.Metadata, Records: fr.Records,
		})
	case model.StatusFailed:
		_ = event.Serial(ctx, emitCtx, events.FileFailed, events.FileFailedPayload{
			Key: key, File: fr.File, Metadata: fr.Metadata, Records: fr.Records, Error: fr.Error,
		})
	}
}

// PublishCollectionResult 发布日期级终结事件。
func PublishCollectionResult(ctx context.Context, emitCtx *runtime.Context, res *model.CollectionResult) {
	if emitCtx == nil || res == nil {
		return
	}
	switch res.Status {
	case model.StatusSucceeded:
		_ = event.Serial(ctx, emitCtx, events.CollectionCompleted, events.CollectionCompletedPayload{
			Key: res.Key, Files: len(res.Files), Records: res.Records, Duration: res.Duration, EndedAt: res.EndedAt,
		})
	case model.StatusFailed:
		_ = event.Serial(ctx, emitCtx, events.CollectionFailed, events.CollectionFailedPayload{
			Key: res.Key, Error: res.Error, Duration: res.Duration,
		})
	case model.StatusPending:
		_ = event.Serial(ctx, emitCtx, events.CollectionPending, events.CollectionPendingPayload{
			Key: res.Key, Note: res.Error, At: res.EndedAt,
		})
	case model.StatusSkipped:
		// 实例制：无数据的日期不物化台账，也不入事件流——结果里的
		// Skipped 只是日志语义，读取方（日历/列表）不应见到伪任务。
	}
}

// Publish emits file and collection outcome events for one collection result.
// Handlers observe through the Application Query Adapter; this function never
// depends on an observer. 即时链路请用 Publish*Started/FileResult/Result 细
// 粒度函数；本批量形态保留给一次性补发场景。
func Publish(ctx context.Context, emitCtx *runtime.Context, res *model.CollectionResult) {
	if emitCtx == nil || res == nil {
		return
	}
	for i := range res.Files {
		PublishFileResult(ctx, emitCtx, res.Key, &res.Files[i])
	}
	PublishCollectionResult(ctx, emitCtx, res)
}
