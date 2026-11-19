package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	appbackup "gocordis-csv-collector/app/backup"
	datepolicy "gocordis-csv-collector/app/date"
	"gocordis-csv-collector/app/errs"
	appmetadata "gocordis-csv-collector/app/metadata"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/recovery"
	"gocordis-csv-collector/internal/hostgate"
)

type Config struct {
	BatchSize  int
	DatePolicy datepolicy.Policy
	// Boundary 是业务日的硬边界，Handle 入口校验一次，内部不再重复判断：
	// 自动采集不越过 Cutoff（dated 源就是昨天），未来日期任何路径都不许，
	// 自动目标早于补采窗口首日同样拒绝。零值 = 只禁未来。
	Boundary datepolicy.Boundary
	// Since 是源的生命周期下界：早于它的业务日不计划、不物化。
	// 零值 = 无下界。
	Since  model.CollectionDate
	Now    func() time.Time
	Logger *slog.Logger
	// FilesPerSource 是单源内文件级并发上限（默认 1 = 串行）。
	FilesPerSource int
	// BackupDir 非空时，文件在标记完成前先复制一份到
	// <BackupDir>/<source_id>/<业务日>/；备份失败即文件失败（严格语义，
	// 下轮重排重试）。BackupKeepDays>0 时每次采集后清理更早的日期目录。
	BackupDir      string
	BackupKeepDays int
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.FilesPerSource <= 0 {
		c.FilesPerSource = 1
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Observer 接收采集过程的即时事件（并发安全由实现方负责；可为 nil）。
// 事件在发生点发布，而不是整次 Handle 之后——多源并行时这是唯一能让
// 观察流保有真实时间线的形态。
type Observer interface {
	CollectionStarted(ctx context.Context, key model.CollectionKey)
	FileStarted(ctx context.Context, key model.CollectionKey, file model.FileIdentity)
	FileDone(ctx context.Context, key model.CollectionKey, fr *model.FileResult)
	CollectionDone(ctx context.Context, res *model.CollectionResult)
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
	// Observer 非 nil 时，采集事件在发生点即时发布（见 Observer）。
	Observer Observer
}

func (e *Executor) observe(f func(o Observer)) {
	if e.Observer != nil {
		f(e.Observer)
	}
}

// endState 必须在作业 ctx 已取消时也能落账：Running 行会被 Begin 的 24h
// 租约和 ListIncomplete 同时挡住，只有进程重启才会回收。终态写入用一个
// 脱钩的短超时 ctx。
//
// 返回错误时调用方必须改变结论：台账没记下这次终结，报成功就是把一个持久化
// 台账里根本不存在的业务日当成已完成——planner 不再排它，Begin 的成功守卫
// 拒绝重开。数据库写入是幂等的，判失败、下轮重放比判成功安全。非成功的终态
// 没有这个风险（结论本来就是失败），调用方只记日志。
func (e *Executor) endState(ctx context.Context, key model.CollectionKey, status model.Status, note string) error {
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := e.State.End(endCtx, key, status, note); err != nil {
		e.Config.withDefaults().Logger.Warn("end collection state failed",
			"key", key.String(), "status", string(status), "error", err.Error())
		return err
	}
	return nil
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
	// 边界只在这一点检查一次：Planner 之后再加窗口过滤会被下面的 hasTarget
	// 原样绕过（Plan 没排出 target 时它还会补一个），DatePolicy 里检查则管不到
	// 手动传入的日期。放在这里，两条入口共用同一条闸。
	if err := cfg.Boundary.Check(target); err != nil {
		cfg.Logger.Error("collection refused by date boundary",
			"source", e.Source.ID(), "target", target.String(), "error", err.Error())
		return nil, err
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
	// 宿主机健康闸的聚合语义：UNC 宿主不可达时只把第一个计划日期记为
	// Failed（携带宿主级原因），其余日期本轮不物化、下轮随 catchup 自然
	// 重排——否则一次网络故障就是 源数×窗口天数 的失败洪水，"需要处理"
	// 不可读。宿主恢复后各日期照常补采。
	skipRest := false
	for i, key := range keys {
		if skipRest {
			cfg.Logger.Warn("unc host unreachable; remaining dates deferred to next pass",
				"source", string(e.Source.ID()), "date", key.Date.String())
			continue
		}
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if i > 0 {
			h := hostgate.HostOf(e.Source.Root())
			if h == "" {
				if rr, ok := e.Source.(interface{ RawRoot() string }); ok {
					h = hostgate.HostOf(rr.RawRoot())
				}
			}
			if h != "" && !hostgate.Reachable(ctx, h) {
				skipRest = true
				cfg.Logger.Warn("unc host unreachable; deferring remaining dates to next pass",
					"source", string(e.Source.ID()), "host", h, "date", key.Date.String())
				continue
			}
		}
		res := e.collectOne(ctx, key)
		if res != nil {
			results = append(results, *res)
			// 即时事件：本日期终结即发布，不等整个 Handle（多源并行时
			// Handle 的时长不再决定观察流的时间线）。
			e.observe(func(o Observer) { o.CollectionDone(ctx, res) })
			if res.Status == model.StatusFailed {
				runErrs = append(runErrs, fmt.Errorf("collection %s failed: %s", key, res.Error))
			}
		}
	}
	if cfg.BackupDir != "" && cfg.BackupKeepDays > 0 {
		appbackup.Prune(cfg.BackupDir, string(e.Source.ID()), cfg.BackupKeepDays, cfg.Now())
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
			// 非 Succeeded 的旧记录（旧进程留下的 Running/Failed、孤儿
			// 失败账）不再是证据：清掉并通知观察层退役 UI 行；Succeeded
			// 是历史事实，即使上游已删除目录也保留。
			if st, existed, sErr := e.State.StatusOf(ctx, key); sErr == nil && !(existed && st == model.StatusSucceeded) {
				result.Dropped = existed
				_ = e.State.Drop(ctx, key)
			}
			result.Status = model.StatusSkipped
			result.Error = "no data for this date"
			return result
		case errs.Is(err, errs.ErrFileUnstable):
			// 候选文件都在稳定窗口内：不物化，下次触发重探——
			// 终态化会把晚到文件永久丢失。
			result.Status = model.StatusPending
			result.Error = fmt.Sprintf("files inside stable window: %v", err)
			// 旧进程中断留下的 Running 必须落成 Pending：否则重启后
			// 那一行永远停在 Running，Begin 的租约还会挡住后续重试。
			if st, ok, sErr := e.State.StatusOf(ctx, key); sErr == nil && ok && st == model.StatusRunning {
				_ = e.endState(ctx, key, model.StatusPending, "waiting for files to become stable")
			}
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
				_ = e.endState(ctx, key, model.StatusFailed, result.Error)
			}
			cfg.Logger.Error("source list failed", "key", key.String(), "error", result.Error)
			return result
		}
	}
	if len(files) == 0 {
		// 没有任何候选文件 = 该日期没有数据（或文件已被上游删除）。
		// 非 Succeeded 的旧记录（Running/Failed/Pending，以及只有失败账的
		// 孤儿键）不再是证据：Drop 掉整份台账，并通知观察层退役 UI 行。
		// Succeeded 是历史事实，append 模式重触发时保留。
		if st, ok, sErr := e.State.StatusOf(ctx, key); sErr == nil && !(ok && st == model.StatusSucceeded) {
			result.Dropped = ok
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
	e.observe(func(o Observer) { o.CollectionStarted(ctx, key) })
	defer func() {
		if result.Status == model.StatusRunning {
			status := model.StatusFailed
			note := "unfinished collection"
			if ctx.Err() != nil {
				status = model.StatusPending
				note = "collection canceled; will re-probe"
			}
			result.Status = status
			result.Error = note
			_ = e.endState(ctx, key, status, note)
		}
	}()

	var failErrs []error
	collectOne := func(i int) model.FileResult { return e.collectFileTracked(ctx, key, &files[i]) }
	if cfg.FilesPerSource <= 1 || len(files) <= 1 {
		// 串行：发现顺序，取消即中止（原语义）。
		for i := range files {
			if err := ctx.Err(); err != nil {
				result.Status = model.StatusPending
				result.Error = "collection canceled; will re-probe"
				_ = e.endState(ctx, key, model.StatusPending, result.Error)
				return result
			}
			fr := collectOne(i)
			result.Files = append(result.Files, fr)
			if fr.Status == model.StatusFailed {
				failErrs = append(failErrs, fmt.Errorf("%s: %s", files[i].Name, fr.Error))
				continue
			}
			result.Records += fr.Records
		}
	} else {
		// 扇出文件级：结果按发现顺序扇入；被取消的文件保持零值
		// （Pending）——与串行一致，未处理的文件不进台账不进事件。
		out := make([]model.FileResult, len(files))
		sem := make(chan struct{}, cfg.FilesPerSource)
		var wg sync.WaitGroup
		for i := range files {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
				}
				if ctx.Err() != nil {
					return
				}
				out[i] = collectOne(i)
			}(i)
		}
		wg.Wait()
		for i := range out {
			fr := out[i]
			// 零值 = 取消时未执行（Status 为 ""）；与串行一致，
			// 未处理的文件不进台账不进事件。
			if fr.File.Name == "" {
				continue
			}
			result.Files = append(result.Files, fr)
			if fr.Status == model.StatusFailed {
				failErrs = append(failErrs, fmt.Errorf("%s: %s", files[i].Name, fr.Error))
				continue
			}
			result.Records += fr.Records
		}
		if err := ctx.Err(); err != nil {
			result.Status = model.StatusPending
			result.Error = "collection canceled; will re-probe"
			_ = e.endState(ctx, key, model.StatusPending, result.Error)
			return result
		}
	}
	result.EndedAt = time.Now()
	result.Duration = result.EndedAt.Sub(started)
	if len(failErrs) > 0 {
		result.Status = model.StatusFailed
		result.Error = errors.Join(failErrs...).Error()
		status := model.StatusFailed
		note := result.Error
		if ctx.Err() != nil {
			status = model.StatusPending
			note = "collection canceled; will re-probe: " + note
		}
		_ = e.endState(ctx, key, status, note)
		cfg.Logger.Error("collection failed", "key", key.String(), "error", result.Error, "records", result.Records, "duration", result.Duration)
		return result
	}
	result.Status = model.StatusSucceeded
	if err := e.endState(ctx, key, model.StatusSucceeded, ""); err != nil {
		// 成功没落账就不是成功：数据行已提交，下轮按幂等重放补齐台账。
		result.Status = model.StatusFailed
		result.Error = fmt.Sprintf("record succeeded: %v", err)
		cfg.Logger.Error("collection succeeded but the ledger write failed; reporting failure for replay",
			"key", key.String(), "records", result.Records, "error", result.Error)
		return result
	}
	cfg.Logger.Info("collection completed", "key", key.String(), "files", len(result.Files), "records", result.Records, "duration", result.Duration)
	return result
}

// collectFileTracked 采集单文件并在其终结点即时发布 FileDone 事件。
func (e *Executor) collectFileTracked(ctx context.Context, key model.CollectionKey, file *model.FileIdentity) model.FileResult {
	fr := e.collectFile(ctx, key, file)
	e.observe(func(o Observer) { o.FileDone(ctx, key, &fr) })
	return fr
}

// completedRecordCount 取回已完成文件的持久化行数。查不到时返回 0，
// 不因读模型展示失败而把整次采集判失败。
func (e *Executor) completedRecordCount(ctx context.Context, key model.CollectionKey, file model.FileIdentity) int64 {
	// 取消路径同样要取到行数：否则 FileDone 带 0 又会把读模型覆盖成 0。
	readCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	recs, err := e.State.FileRecords(readCtx, key)
	if err != nil {
		e.Config.withDefaults().Logger.Warn("read completed file record count failed",
			"key", key.String(), "file", file.Name, "error", err.Error())
		return 0
	}
	id := file.Identity()
	for i := range recs {
		if recs[i].File.Identity() == id {
			return recs[i].Records
		}
	}
	return 0
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
		// 已完成的文件在重试/append 触发时会被重新扫到。带 0 发
		// FileCompleted 会把读模型里的真实行数覆盖成 0（重启投影才恢复），
		// 所以这里取回持久化行数，保持事件载荷与台账一致。
		fr.Records = e.completedRecordCount(ctx, key, *file)
		return fr
	}
	cfg.Logger.Info("file started", "key", key.String(), "file", file.Name)
	e.observe(func(o Observer) { o.FileStarted(ctx, key, *file) })
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
	// 严格备份语义：本机副本落盘成功才允许标记完成；失败走 recordFail，
	// 文件转 Failed 由下轮重排（写库幂等使重跑安全）。放在
	// MarkFileCompleted 之后不可行——FileCompleted 守卫会让"已成功未
	// 备份"的文件被永久短路。
	if cfg.BackupDir != "" {
		dateDir := filepath.Join(e.Source.Root(), key.Date.String())
		if err := appbackup.File(cfg.BackupDir, string(key.SourceID), key.Date.String(), dateDir, *file); err != nil {
			fr.Status = model.StatusFailed
			fr.Error = err.Error()
			cfg.Logger.Error("file backup failed", "key", key.String(), "file", file.Name, "error", fr.Error)
			recordFail()
			return fr
		}
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
