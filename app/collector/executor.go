package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/errs"
	appmetadata "gocordis-csv-collector/app/metadata"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/recovery"
)

type Config struct {
	BatchSize  int
	DatePolicy datepolicy.Policy
	// CatchupDays bounds how many calendar days one trigger reaches back when
	// the state chain has no recent success (first deployment or lost state).
	// Zero keeps the previous behavior: gaps are synthesized only from the
	// last succeeded business date.
	CatchupDays int
	// Since 是源的生命周期下界：早于它的业务日不计划、不物化。
	// 零值 = 无下界。
	Since  model.CollectionDate
	Now    func() time.Time
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

type Executor struct {
	Source            model.FileSource
	Parser            model.CSVParser
	Storage           model.Storage
	State             model.CollectionState
	MetadataExtractor model.MetadataExtractor
	// SourceMetadata is the static business metadata owned by the configured
	// Source. It is overlaid after path/CSV document interpretation, so an
	// explicit source table (machine/line/plant) can never be overwritten by
	// file-derived metadata.
	SourceMetadata model.Metadata

	Recovery recovery.Planner
	Config   Config
}

func (e *Executor) Handle(ctx context.Context, req model.CollectionRequested) ([]model.CollectionResult, error) {
	cfg := e.Config.withDefaults()
	target, err := cfg.DatePolicy.Resolve()
	if err != nil {
		return nil, err
	}
	if req.Date != nil {
		target = *req.Date
	}

	keys, err := e.Recovery.Plan(ctx, e.Source.ID(), target)
	if err != nil {
		return nil, err
	}
	// 生命周期下界：早于 since 的业务日不计划、不物化（显式指定日期的
	// 手动触发同样遵守——since 是源的存在性事实，不是策略偏好）。
	if !cfg.Since.IsZero() {
		filtered := keys[:0]
		for _, k := range keys {
			if !k.Date.Before(cfg.Since) {
				filtered = append(filtered, k)
			}
		}
		keys = filtered
		if target.Before(cfg.Since) {
			cfg.Logger.Info("target date is before source since; nothing to collect",
				"source", e.Source.ID(), "target", target.String(), "since", cfg.Since.String())
			return []model.CollectionResult{{
				Key:    model.CollectionKey{SourceID: e.Source.ID(), Date: target},
				Status: model.StatusSkipped, Error: "target date is before source since",
			}}, nil
		}
	}
	hasTarget := false
	for _, k := range keys {
		if k.Date.Equal(target) {
			hasTarget = true
			break
		}
	}
	if !hasTarget {
		keys = append(keys, model.CollectionKey{SourceID: e.Source.ID(), Date: target})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Date.Before(keys[j].Date) })

	var results []model.CollectionResult
	var runErrs []error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		res := e.collectOne(ctx, key)
		if res != nil {
			results = append(results, *res)
			if res.Status == model.StatusFailed {
				runErrs = append(runErrs, fmt.Errorf("collection %s failed: %s", key, res.Error))
			}
		}
	}
	if len(runErrs) > 0 {
		return results, errors.Join(runErrs...)
	}
	return results, nil
}

// collectOne 是实例制的核心：先探针（List），有证据才物化台账。
// 文件不存在 = 当天没有数据 = 不产生任何记录。
func (e *Executor) collectOne(ctx context.Context, key model.CollectionKey) *model.CollectionResult {
	cfg := e.Config.withDefaults()
	started := time.Now()
	if cfg.Now != nil {
		started = cfg.Now()
	}
	result := &model.CollectionResult{Key: key, StartedAt: started, Status: model.StatusRunning}
	cfg.Logger.Info("collection started", "source_id", key.SourceID, "date", key.Date.String(), "key", key.String())
	files, err := e.Source.List(ctx, model.ListRequest{SourceID: key.SourceID, Date: key.Date})
	if err != nil {
		switch {
		case errs.Is(err, errs.ErrNotFound):
			// 文件不存在 = 无数据 = 不物化。没有实例、没有重试、没有
			// 告警——"没有"就是没有。这不是失败，只是当天没有数据。
			_ = e.State.Drop(ctx, key)
			result.Status = model.StatusSkipped
			result.Error = "no data for this date"
			return result
		case errs.Is(err, errs.ErrFileUnstable):
			// 候选文件都在稳定窗口内：不物化，下次触发重探——
			// 终态化会把晚到文件永久丢失。
			result.Status = model.StatusPending
			result.Error = fmt.Sprintf("files inside stable window: %v", err)
			cfg.Logger.Warn("files unstable; will re-probe next trigger", "key", key.String())
			return result
		default:
			// 不可达等暂时故障：可重试的 Failed 实例，ListIncomplete 会重排。
			result.Status = model.StatusFailed
			result.Error = err.Error()
			if ok, berr := e.State.Begin(ctx, key, 24*time.Hour); berr != nil {
				result.Status = model.StatusFailed
				result.Error = fmt.Sprintf("begin state: %v", berr)
				return result
			} else if ok {
				_ = e.State.End(ctx, key, model.StatusFailed, result.Error)
			}
			cfg.Logger.Error("source list failed", "key", key.String(), "error", result.Error)
			return result
		}
	}
	if len(files) == 0 {
		if st, ok, sErr := e.State.StatusOf(ctx, key); sErr == nil && ok &&
			(st == model.StatusSkipped || st == model.StatusPending) {
			_ = e.State.Drop(ctx, key)
		}
		result.Status = model.StatusSkipped
		result.Error = "no files matched"
		cfg.Logger.Info("no files matched; not materialized", "key", key.String())
		return result
	}

	ok, err := e.State.Begin(ctx, key, 24*time.Hour)
	if err != nil {
		result.Status = model.StatusFailed
		result.Error = fmt.Sprintf("begin state: %v", err)
		return result
	}
	if !ok {
		cfg.Logger.Debug("collection skipped", "key", key.String())
		return nil
	}
	defer func() {
		if result.Status == model.StatusRunning {
			result.Status = model.StatusFailed
			result.Error = "unfinished collection"
			_ = e.State.End(ctx, key, model.StatusFailed, result.Error)
		}
	}()

	var failErrs []error
	for i := range files {
		if err := ctx.Err(); err != nil {
			result.Status = model.StatusFailed
			result.Error = err.Error()
			_ = e.State.End(ctx, key, model.StatusFailed, result.Error)
			return result
		}
		fr := e.collectFile(ctx, key, &files[i])
		result.Files = append(result.Files, fr)
		if fr.Status == model.StatusFailed {
			failErrs = append(failErrs, fmt.Errorf("%s: %s", files[i].Name, fr.Error))
			continue
		}
		result.Records += fr.Records
	}
	result.EndedAt = time.Now()
	result.Duration = result.EndedAt.Sub(started)
	if len(failErrs) > 0 {
		result.Status = model.StatusFailed
		result.Error = errors.Join(failErrs...).Error()
		_ = e.State.End(ctx, key, model.StatusFailed, result.Error)
		cfg.Logger.Error("collection failed", "key", key.String(), "error", result.Error, "records", result.Records, "duration", result.Duration)
		return result
	}
	result.Status = model.StatusSucceeded
	_ = e.State.End(ctx, key, model.StatusSucceeded, "")
	cfg.Logger.Info("collection completed", "key", key.String(), "files", len(result.Files), "records", result.Records, "duration", result.Duration)
	return result
}

func (e *Executor) collectFile(ctx context.Context, key model.CollectionKey, file *model.FileIdentity) model.FileResult {
	cfg := e.Config.withDefaults()
	fr := model.FileResult{File: *file, Status: model.StatusPending}
	// recordFail persists the local failure ledger entry. It is best effort: a
	// state write failure must not mask the original failure being reported.
	recordFail := func() {
		if err := e.State.MarkFileFailed(ctx, key, *file, fr.Error); err != nil {
			cfg.Logger.Warn("record file failure failed", "key", key.String(), "file", file.Name, "error", err.Error())
		}
	}
	cfg.Logger.Info("file discovered", "key", key.String(), "file", file.Name)
	done, err := e.State.FileCompleted(ctx, key, *file)
	if err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		recordFail()
		return fr
	}
	if done {
		cfg.Logger.Debug("file already processed", "key", key.String(), "file", file.Name)
		fr.Status = model.StatusSucceeded
		return fr
	}
	cfg.Logger.Info("file started", "key", key.String(), "file", file.Name)
	md := model.NewMetadata()
	if e.MetadataExtractor != nil {
		var err error
		md, err = e.MetadataExtractor.Extract(ctx, *file)
		if err != nil {
			fr.Status = model.StatusFailed
			fr.Error = err.Error()
			cfg.Logger.Error("file metadata extraction failed", "key", key.String(), "file", file.Name, "error", fr.Error)
			recordFail()
			return fr
		}
	}
	md = overlayMetadata(md, e.SourceMetadata)
	fr.Metadata = md.Clone()
	rc, err := e.Source.Read(ctx, *file)
	if err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		cfg.Logger.Error("file read open failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		recordFail()
		return fr
	}
	defer rc.Close()
	doc, err := e.Parser.Parse(ctx, rc)
	if err != nil {
		fr.Status = model.StatusFailed
		fr.Error = fmt.Sprintf("source %s file %q: %v", e.Source.ID(), file.Name, err)
		cfg.Logger.Error("file parse failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		recordFail()
		return fr
	}
	md = appmetadata.MergeDocument(md, doc)
	md = overlayMetadata(md, e.SourceMetadata)
	fr.Metadata = md.Clone()
	stream := doc.Data
	header := stream.Header()
	var records []model.Record
	sequence := 0
	write := func() error {
		if len(records) == 0 {
			return nil
		}
		sequence++
		batch := model.Batch{Key: key, File: *file, Metadata: md.Clone(), Records: records, Sequence: sequence, Header: header, CreatedAt: time.Now()}
		if err := e.Storage.Write(ctx, batch); err != nil {
			return err
		}
		fr.Records += int64(len(records))
		records = records[:0]
		return nil
	}
	for {
		rec, err := stream.Next()
		if err == model.ErrEOF {
			break
		}
		if err != nil {
			_ = write()
			fr.Status = model.StatusFailed
			fr.Error = fmt.Sprintf("source %s file %q: %v", e.Source.ID(), file.Name, err)
			cfg.Logger.Error("file parse row failed", "key", key.String(), "file", file.Name, "error", fr.Error)
			recordFail()
			return fr
		}
		records = append(records, rec)
		if len(records) >= cfg.BatchSize {
			if err := write(); err != nil {
				fr.Status = model.StatusFailed
				fr.Error = err.Error()
				cfg.Logger.Error("storage write failed", "key", key.String(), "file", file.Name, "error", fr.Error)
				recordFail()
				return fr
			}
		}
	}
	if err := write(); err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		cfg.Logger.Error("storage write failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		recordFail()
		return fr
	}
	if err := e.State.MarkFileCompleted(ctx, key, *file, fr.Records); err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		cfg.Logger.Error("mark file completed failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		recordFail()
		return fr
	}
	fr.Status = model.StatusSucceeded
	cfg.Logger.Info("file completed", "key", key.String(), "file", file.Name, "records", fr.Records)
	return fr
}

func overlayMetadata(base model.Metadata, source model.Metadata) model.Metadata {
	if source.Len() == 0 {
		return base
	}
	out := base.Clone()
	for k, v := range source.Values {
		out.Values[k] = v
	}
	return out
}
