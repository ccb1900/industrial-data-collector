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
	// NoDataGraceHours 已由实例制取代：缺失可见性由巡检（Expect/
	// InspectLookbackDays）承担。字段保留用于兼容旧配置解析，不再参与
	// 语义。
	NoDataGraceHours int
	// Since 是源的生命周期下界：早于它的业务日不计划、不巡检、不物化。
	// 零值 = 无下界。
	Since model.CollectionDate
	// Expect 声明预期节奏（"daily" = 每个业务日应有数据）。空 = 不巡检，
	// 缺失不物化。
	Expect string
	// InspectLookbackDays 巡检回看窗口（含目标日，默认 1 = 只看目标日）。
	InspectLookbackDays int
	Now                 func() time.Time
	Logger              *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.NoDataGraceHours <= 0 {
		c.NoDataGraceHours = 6
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

// InspectionDue 报告一个业务日是否在巡检回看窗口内：expect 声明了预期
// 节奏时，窗口内（以策略目标日为锚，往回数 lookback 天）的日期"该有而
// 没有"要生成可重试的异常实例；窗口外与未声明预期的日期缺失不物化——
// 缺失可见性由巡检承担，而不是台账。今天本身不在窗口内：今天的数据
// 可能合法地尚未产生。
func InspectionDue(cfg Config, date, anchor model.CollectionDate) bool {
	if cfg.Expect == "" {
		return false
	}
	lookback := cfg.InspectLookbackDays
	if lookback < 1 {
		lookback = 1
	}
	earliest := anchor.AddDate(0, 0, -(lookback - 1))
	return !date.Before(earliest) && !date.After(anchor)
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
			return nil, nil
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
		res := e.collectOne(ctx, key, target)
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
//   - 有文件        → Begin → 采集 → Succeeded/Failed（可重试实例）
//   - 无文件 + 巡检窗口内（声明了 expect）→ Failed 异常实例（下次巡检
//     复查：文件到了则采集成功覆盖它）
//   - 无文件 + 窗口外/未声明预期 → 不物化，并 Drop 历史遗留记录
//   - 不稳定/不可达  → 不物化或可重试 Failed，由下次触发重探
//
// Pending 与 Skipped 不再产生：缺失可见性由巡检承担，台账只保留真实
// 发生过的工作。
func (e *Executor) collectOne(ctx context.Context, key model.CollectionKey, anchor model.CollectionDate) *model.CollectionResult {
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
			// 巡检窗口内的预期缺失：可重试的 Failed 实例（文件晚到后
			// 下次采集成功即覆盖）。
			if InspectionDue(cfg, key.Date, anchor) {
				result.Status = model.StatusFailed
				result.Error = fmt.Sprintf("inspection: expected %s data is missing: %v", cfg.Expect, err)
				if ok, berr := e.State.Begin(ctx, key, 24*time.Hour); berr != nil {
					result.Status = model.StatusFailed
					result.Error = fmt.Sprintf("begin state: %v", berr)
					return result
				} else if ok {
					_ = e.State.End(ctx, key, model.StatusFailed, result.Error)
				}
				cfg.Logger.Warn("inspection: expected data missing", "key", key.String(), "error", err.Error())
				result.EndedAt = time.Now()
				result.Duration = result.EndedAt.Sub(started)
				return result
			}
			// 无证据不物化：清掉旧语义遗留的半截记录（Skipped/Pending）。
			_ = e.State.Drop(ctx, key)
			result.Status = model.StatusSkipped
			result.Error = fmt.Sprintf("no data; not materialized: %v", err)
			cfg.Logger.Info("no data for date; not materialized", "key", key.String())
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
		_ = e.State.Drop(ctx, key)
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
