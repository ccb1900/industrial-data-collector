package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
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
	// NoDataConfirmDays 是"无数据确认"的天数阈值（0 = 关闭，默认）：共享可达
	// 但同一业务日连续 N 次探不到文件时，把该日终态化为有证据的 Skipped——
	// 假期/停产不再每晚刷"待探"。关闭时保持实例制语义：不落账、窗口重扫
	// 每晚再探，晚到文件总能被捡回来。
	NoDataConfirmDays int
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
	// CollectionsExpired 报告本轮退役的业务日（离开自动补采窗口、不再自动重试）。
	// 退役是在扫描台账时发现的，不是一次采集的结果，所以它不带 CollectionResult。
	CollectionsExpired(ctx context.Context, keys []model.CollectionKey)
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

// settleNoData 把一次"探不到"计入确认计数；达到阈值时终态化为有证据的
// Skipped。两个"没有"的形态共用它：目录/文件不存在（ErrNotFound）与目录
// 存在但没有匹配文件（空列表）——上游先建目录后写文件是常见行为。
// 返回 true 表示结果已写好，调用方直接返回。
// 终态写失败时撤销刚 Begin 的行：留给它的 24h 租约会把这个日期冻住一整天，
// "下一轮重新判定"得真的有下一轮能排它才行。
func (e *Executor) settleNoData(ctx context.Context, key model.CollectionKey, cfg Config, result *model.CollectionResult) bool {
	if cfg.NoDataConfirmDays <= 0 {
		return false
	}
	confirmed, perr := e.State.RecordNoDataProbe(ctx, key, cfg.NoDataConfirmDays)
	if perr != nil {
		cfg.Logger.Warn("no-data probe accounting failed; treating as unconfirmed",
			"key", key.String(), "error", perr.Error())
		return false
	}
	if !confirmed {
		return false
	}
	note := fmt.Sprintf("no data confirmed after %d probes", cfg.NoDataConfirmDays)
	if ok, berr := e.State.Begin(ctx, key, 24*time.Hour); berr == nil && ok {
		if eerr := e.endState(ctx, key, model.StatusSkipped, note); eerr == nil {
			result.NoDataConfirmed = true
		} else if derr := e.State.Drop(ctx, key); derr != nil {
			cfg.Logger.Warn("no-data settle rollback failed; the date may sit in Running for its lease",
				"key", key.String(), "error", derr.Error())
		}
	}
	if result.NoDataConfirmed {
		result.Status = model.StatusSkipped
		result.Error = "no data confirmed"
		return true
	}
	return false
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
	cfg := e.Config.withDefaults()
	// 瞬时失败（SQLITE_BUSY、杀软短暂持句柄）必须先重试再下结论：放弃一次
	// 50ms 就能好的写入，行会停在 Running，而 24h 租约同时挡住 ListIncomplete
	// 与 Begin——一个 busy 就此变成"这一整天不再被重试"。
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-endCtx.Done():
			}
		}
		if err = e.State.End(endCtx, key, status, note); err == nil {
			return nil
		}
		cfg.Logger.Warn("end collection state failed",
			"key", key.String(), "status", string(status), "attempt", attempt+1, "error", err.Error())
	}
	return err
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
	manual := req.Date != nil
	// 边界只在这一点检查一次：Planner 之后再加窗口过滤会被下面的 hasTarget
	// 原样绕过（Plan 没排出 target 时它还会补一个），DatePolicy 里检查则管不到
	// 手动传入的日期。放在这里，两条入口共用同一条闸。
	if err := cfg.Boundary.Check(target); err != nil {
		cfg.Logger.Error("collection refused by date boundary",
			"source", e.Source.ID(), "target", target.String(), "error", err.Error())
		return nil, err
	}

	// 退役扫描锚在 Cutoff（昨天/今天），不锚在本轮 target：手动回填一个半年前的
	// 日期时，"自动窗口"依然是从今天往回 30 天。planner 的 target 窗口决定"这轮
	// 探哪些天"，这里决定"哪些旧账不再自动试"，两者都由 Boundary 这一处口径算出。
	if start, ok := cfg.Boundary.Start(); ok {
		retired, err := e.State.ExpireBeyond(ctx, e.Source.ID(), start)
		if err != nil {
			// 扫描失败只是这一轮继续重试超期日期，与改造前一致；不能因为一次
			// 台账读失败把本轮该采的日期也判死。
			cfg.Logger.Warn("expire sweep failed; out-of-window rows stay retryable this pass",
				"source", e.Source.ID(), "error", err.Error())
		} else if len(retired) > 0 {
			cfg.Logger.Info("retired business dates beyond the catch-up window",
				"source", e.Source.ID(), "count", len(retired), "window_start", start.String())
			e.observe(func(o Observer) { o.CollectionsExpired(ctx, retired) })
		}
	}

	var keys []model.CollectionKey
	if manual {
		// 手动指定日期的语义是"就采这个日期"：不再顺带规划整个窗口。窗口
		// 重扫是自动路径（nightly/startup）的职责——把 30 个日期的探测塞进
		// 一次人工回填，既淹没结果列表，也会把请求日期之前的无数据日期全部
		// 探一遍（开启 no_data_after_days 时甚至全部终态化）。
		keys = append(keys, model.CollectionKey{SourceID: e.Source.ID(), Date: target})
	} else if planned, perr := e.Recovery.Plan(ctx, e.Source.ID(), target); perr != nil {
		return nil, perr
	} else {
		keys = planned
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
		case errs.Is(err, errs.ErrNoNewContent):
			// flat 源 + 内容哈希去重："文件在、内容没变"。结果同 Skipped
			//（不物化、不告警），但不进无数据确认计数、不清任何台账——
			// append 模式下这是"已采过"的证明，不是"当天没有"。
			result.Status = model.StatusSkipped
			result.Error = "no new content (unchanged file)"
			return result
		case errs.Is(err, errs.ErrNotFound):
			// 文件不存在 = 无数据 = 不物化。没有实例、没有重试、没有
			// 告警——"没有"就是没有。这不是失败，只是当天没有数据。
			// 非 Succeeded 的旧记录（旧进程留下的 Running/Failed、孤儿
			// 失败账）不再是证据：清掉并通知观察层退役 UI 行；Succeeded
			// 是历史事实，即使上游已删除目录也保留。
			//
			// 显式开启 no_data_after_days 时，"共享可达但连续 N 次探不到"
			// 升格为有证据的终态：Skipped 行进台账（所有读取面把 Skipped
			// 当已结算历史），事件告知投影。计数落台账，重启不清零；
			// 不可达（ErrUnavailable）走下面 default 分支，绝不参与计数。
			if e.settleNoData(ctx, key, cfg, result) {
				return result
			}
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
			if cfg.NoDataConfirmDays > 0 {
				// 文件存在但还在写："连续探不到"被打断——否则文件持续
				// 处于稳定窗口时计数照涨，会把活跃日误终态化成无数据。
				if rerr := e.State.ResetNoData(ctx, key); rerr != nil {
					cfg.Logger.Warn("no-data streak reset failed", "key", key.String(), "error", rerr.Error())
				}
			}
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
	if len(files) > 0 && cfg.NoDataConfirmDays > 0 {
		// 文件出现了（哪怕还没稳定）："连续探不到"的连续性被打断。
		if rerr := e.State.ResetNoData(ctx, key); rerr != nil {
			cfg.Logger.Warn("no-data streak reset failed", "key", key.String(), "error", rerr.Error())
		}
	}
	if len(files) == 0 {
		// 没有任何候选文件 = 该日期没有数据（或文件已被上游删除）。
		// 非 Succeeded 的旧记录（Running/Failed/Pending，以及只有失败账的
		// 孤儿键）不再是证据：Drop 掉整份台账，并通知观察层退役 UI 行。
		// Succeeded 是历史事实，append 模式重触发时保留。
		// 空目录与目录不存在是同一种"没有"：确认计数一视同仁——上游
		// "先 mkdir 再写文件"是常见行为，目录存在与否不该决定
		// no_data_after_days 是否生效。
		if e.settleNoData(ctx, key, cfg, result) {
			return result
		}
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
	// 文件级退避：失败账一次读入，按文件身份映射。到期才重啃，未到期本轮
	// 跳过——它算"未完成"，绝不能算成功，否则成功守卫会把这一天锁死。
	throttled := map[string]time.Time{}
	if cfg.Now != nil {
		fails, ferr := e.State.ListFileFailures(ctx, key.SourceID)
		if ferr != nil {
			// 读不到失败账就关不掉节流——静默吞掉会让退避悄悄失效。
			cfg.Logger.Warn("read failure ledger failed; retry throttling disabled this pass",
				"key", key.String(), "error", ferr.Error())
		}
		for _, f := range fails {
			if f.Key == key && f.NextAttemptAt.After(cfg.Now()) {
				throttled[f.File.Identity()] = f.NextAttemptAt
			}
		}
	}
	var throttledCount int
	collectOne := func(i int) model.FileResult { return e.collectFileTracked(ctx, key, &files[i], throttled) }
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
			if fr.Status == model.StatusPending && strings.Contains(fr.Error, "retry throttled") {
				throttledCount++
				continue
			}
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
			if fr.Status == model.StatusPending && strings.Contains(fr.Error, "retry throttled") {
				throttledCount++
				continue
			}
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
	if throttledCount > 0 {
		// 有文件在退避期内：这一天保持未完成（Pending），下一轮窗口重扫会再
		// 排它，到期的那部分文件才会被真正重啃。标 Succeeded 会让成功守卫
		// 锁死这一天——退避就变成了永久丢失。
		result.Status = model.StatusPending
		result.Error = fmt.Sprintf("%d file(s) in retry backoff", throttledCount)
		_ = e.endState(ctx, key, model.StatusPending, result.Error)
		cfg.Logger.Info("collection deferred by retry backoff", "key", key.String(), "files", throttledCount)
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
func (e *Executor) collectFileTracked(ctx context.Context, key model.CollectionKey, file *model.FileIdentity, throttled map[string]time.Time) model.FileResult {
	fr := e.collectFile(ctx, key, file, throttled)
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

func (e *Executor) collectFile(ctx context.Context, key model.CollectionKey, file *model.FileIdentity, throttled map[string]time.Time) model.FileResult {
	cfg := e.Config.withDefaults()
	fr := model.FileResult{File: *file, Status: model.StatusPending}
	if cfg.Now != nil {
		if due, ok := throttled[file.Identity()]; ok && due.After(cfg.Now()) {
			// 退避期内：这一轮不碰它。结果是 Pending（不是 Failed——它没有
			// 新错误；也不是省略——文件还在清单里，日期保持未完成）。
			return model.FileResult{
				File: *file, Status: model.StatusPending,
				Error: fmt.Sprintf("retry throttled until %s", due.Format(time.RFC3339)),
			}
		}
	}
	// recordFail persists the local failure ledger entry. It is best effort: a
	// state write failure must not mask the original failure being reported.
	// throttled 只对"文件自身的失败"为真；基础设施失败（库不可达、台账自身
	// 故障）只记账——它们的重试信号是基础设施恢复，"恢复后立即重试"是验收
	// 要求（TestOperationsDatabaseOutageRetriesIdempotently 钉着）。
	recordFail := func(throttled bool) {
		if err := e.State.MarkFileFailed(ctx, key, *file, fr.Error, throttled); err != nil {
			cfg.Logger.Warn("record file failure failed", "key", key.String(), "file", file.Name, "error", err.Error())
		}
	}
	cfg.Logger.Info("file discovered", "key", key.String(), "file", file.Name)
	done, err := e.State.FileCompleted(ctx, key, *file)
	if err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		recordFail(false) // 台账自身故障：不是文件的错，不节流
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
			recordFail(true)
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
		// 打不开可能是共享抖动（ErrUnavailable）：不节流，下一轮立刻再试；
		// 其余（权限、损坏）按文件自身失败节流。
		recordFail(!errors.Is(err, errs.ErrUnavailable))
		return fr
	}
	defer rc.Close()
	doc, err := e.Parser.Parse(ctx, rc)
	if err != nil {
		fr.Status = model.StatusFailed
		fr.Error = fmt.Sprintf("source %s file %q: %v", e.Source.ID(), file.Name, err)
		cfg.Logger.Error("file parse failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		recordFail(true)
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
			recordFail(true)
			return fr
		}
		records = append(records, rec)
		if len(records) >= cfg.BatchSize {
			if err := write(); err != nil {
				fr.Status = model.StatusFailed
				fr.Error = err.Error()
				cfg.Logger.Error("storage write failed", "key", key.String(), "file", file.Name, "error", fr.Error)
				// 存储失败（无论瞬时还是"永久"）的修复信号都是外部恢复——库
				// 回来、DBA 改表、磁盘清出来。"修复后立即重试"是验收要求
				// （TestOperationsDatabaseOutageRetriesIdempotently、
				// TestTableStorageAddColumnEvolution 都钉着），退避它们等于
				// 把修复后的第一次重试推迟到 6 小时后。
				recordFail(false)
				return fr
			}
		}
	}
	if err := write(); err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		cfg.Logger.Error("storage write failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		recordFail(false)
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
			recordFail(false) // 备份盘的问题由外部修复，恢复后立即重试
			return fr
		}
	}
	if err := e.State.MarkFileCompleted(ctx, key, *file, fr.Records); err != nil {
		fr.Status = model.StatusFailed
		fr.Error = err.Error()
		cfg.Logger.Error("mark file completed failed", "key", key.String(), "file", file.Name, "error", fr.Error)
		recordFail(false) // 台账自身故障：不节流
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
