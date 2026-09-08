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
	Now        func() time.Time
	Logger     *slog.Logger
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

func (e *Executor) collectOne(ctx context.Context, key model.CollectionKey) *model.CollectionResult {
	cfg := e.Config.withDefaults()
	started := time.Now()
	if cfg.Now != nil {
		started = cfg.Now()
	}
	result := &model.CollectionResult{Key: key, StartedAt: started, Status: model.StatusRunning}
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

	cfg.Logger.Info("collection started", "source_id", key.SourceID, "date", key.Date.String(), "key", key.String())
	files, err := e.Source.List(ctx, model.ListRequest{SourceID: key.SourceID, Date: key.Date})
	if err != nil {
		if errs.Is(err, errs.ErrNotFound) {
			result.Status = model.StatusPending
			result.Error = fmt.Sprintf("date directory not available: %v", err)
			_ = e.State.End(ctx, key, model.StatusPending, result.Error)
			cfg.Logger.Warn("date directory not found; left pending for retry", "key", key.String(), "error", result.Error)
			return result
		}
		result.Status = model.StatusFailed
		result.Error = err.Error()
		_ = e.State.End(ctx, key, model.StatusFailed, result.Error)
		cfg.Logger.Error("source list failed", "key", key.String(), "error", result.Error)
		return result
	}
	if len(files) == 0 {
		_ = e.State.End(ctx, key, model.StatusSucceeded, "no files")
		result.Status = model.StatusSucceeded
		result.EndedAt = time.Now()
		result.Duration = result.EndedAt.Sub(started)
		cfg.Logger.Info("collection completed with no files", "key", key.String())
		return result
	}

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
	cfg.Logger.Info("file discovered", "key", key.String(), "file", file.Name)
	done, err := e.State.FileCompleted(ctx, key, *file)
	if err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
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
		return fr
	}
	defer rc.Close()
	doc, err := e.Parser.Parse(ctx, rc)
	if err != nil {
		fr.Status = model.StatusFailed
		fr.Error = fmt.Sprintf("source %s file %q: %v", e.Source.ID(), file.Name, err)
		cfg.Logger.Error("file parse failed", "key", key.String(), "file", file.Name, "error", fr.Error)
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
			return fr
		}
		records = append(records, rec)
		if len(records) >= cfg.BatchSize {
			if err := write(); err != nil {
				fr.Status = model.StatusFailed
				fr.Error = err.Error()
				cfg.Logger.Error("storage write failed", "key", key.String(), "file", file.Name, "error", fr.Error)
				return fr
			}
		}
	}
	if err := write(); err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		cfg.Logger.Error("storage write failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		return fr
	}
	if err := e.State.MarkFileCompleted(ctx, key, *file); err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		cfg.Logger.Error("mark file completed failed", "key", key.String(), "file", file.Name, "error", fr.Error)
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
