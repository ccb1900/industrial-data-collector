package sourceunit

// 采集后行列一致性校验：文件是唯一真源。每个采集完成日重新测量文件的
// 行/列矩阵，与台账记录、库中实际行数逐一对照；对不上就落一条判定并广播
// 观察事件，由人工处置。校验本身不改变采集状态、不进入失败重试账——
// 它是证据，不是工作项。

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"dynamic-runtime/runtime"

	appbackup "gocordis-csv-collector/app/backup"
	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/state"
	"gocordis-csv-collector/app/verify"
	"gocordis-csv-collector/components/internal/outcome"
)

// verifyTimeout 是一次采集校验的时间上限（整批文件共用一次预算）。超时不
// 落判定：半途而废的行数不是文件的真实形态，把它写成证据只会误导操作员。
const verifyTimeout = 30 * time.Second

// fileOpener 决定校验读哪一份文件：优先本机备份副本（backup_dir 里那次
// 采集留下的原样文件，读取更快也不再受远端可用性影响），没有副本才回源
// 读原文件。日期目录算式必须与采集写备份时一致，否则两边算出的路径漂移，
// 校验会静默退回远端。
type fileOpener struct {
	src       model.FileSource
	backupDir string
}

func (o fileOpener) Open(ctx context.Context, key model.CollectionKey, file model.FileIdentity) (io.ReadCloser, error) {
	dateDir := filepath.Join(o.src.Root(), key.Date.String())
	if p := appbackup.Path(o.backupDir, string(key.SourceID), key.Date.String(), dateDir, file); p != "" {
		if f, err := os.Open(p); err == nil {
			return f, nil
		}
	}
	return o.src.Read(ctx, file)
}

// checkRunner 是一次激活里的校验执行体。判定所需三样能力（文件重测器、
// 库侧计数、文件读取器）都在 Apply 内装配：sink 连接随激活开合，不能跨
// 代复用。
type checkRunner struct {
	checker verify.Checker
	store   state.CheckStore
	emitCtx *runtime.Context
	logger  *slog.Logger
}

func (r *checkRunner) run(ctx context.Context, key model.CollectionKey, files []model.FileRecordView, triggeredBy string) []model.FileCheck {
	checks := r.checker.CheckCollection(ctx, key, files)
	if len(checks) == 0 {
		return nil
	}
	if ctx.Err() != nil {
		r.logger.Warn("consistency check aborted before completion; verdicts not recorded",
			"key", key.String(), "files", len(files), "reason", ctx.Err().Error())
		return nil
	}
	if allSkipped(checks) {
		// 本源形态不可测（结构化 csv.metadata 布局）：不写判定，
		// 否则台账里只有一堆永久 Skipped 的噪声行。
		r.logger.Debug("consistency check produced no verdict", "key", key.String(), "note", checks[0].Note)
		return nil
	}
	if r.store != nil {
		if err := r.store.RecordChecks(ctx, checks); err != nil {
			r.logger.Warn("record consistency checks failed", "key", key.String(), "error", err.Error())
		}
	}
	outcome.PublishChecksVerified(ctx, r.emitCtx, key, checks, triggeredBy)
	s := verify.Summarize(checks)
	fields := []any{"key", key.String(), "files", s.Checked, "ok", s.OK, "warning", s.Warning,
		"skipped", s.Skipped, "trigger", triggeredBy}
	if s.Mismatch+s.Error > 0 {
		fields = append(fields, "mismatch", s.Mismatch, "error", s.Error)
		r.logger.Warn("consistency check found disagreements", fields...)
		return checks
	}
	r.logger.Info("consistency check passed", fields...)
	return checks
}

// runResults 校验一次采集覆盖的每个日期。只测真正入库的文件：采集失败的
// 文件没有落库期望，重测它得不到可比对的两侧。
func (r *checkRunner) runResults(ctx context.Context, results []model.CollectionResult) {
	for i := range results {
		if ctx.Err() != nil {
			return
		}
		files := completedFiles(&results[i])
		if len(files) == 0 {
			continue
		}
		r.run(ctx, results[i].Key, files, "collect")
	}
}

func completedFiles(res *model.CollectionResult) []model.FileRecordView {
	out := make([]model.FileRecordView, 0, len(res.Files))
	for _, fr := range res.Files {
		if fr.Status != model.StatusSucceeded {
			continue
		}
		out = append(out, model.FileRecordView{Key: res.Key, File: fr.File, Records: fr.Records})
	}
	return out
}

func allSkipped(checks []model.FileCheck) bool {
	for _, c := range checks {
		if c.Status != model.CheckSkipped {
			return false
		}
	}
	return true
}

// newCheckRunner 装配本激活的校验执行体；返回 nil 表示本源不校验：配置
// 关闭，或解析器不具备行列重测能力（纯文本解析器的"行"与文件行不同构，
// 重测出来的矩阵无法与落库结果对照）。
func (c *SourceUnitComponent) newCheckRunner(store model.Storage) *checkRunner {
	if !c.verifyAfterCollect {
		return nil
	}
	if _, ok := c.parser.(verify.Shaper); !ok {
		c.logger.Info("consistency check unavailable for this parser", "source", string(c.sourceID))
		return nil
	}
	sink, _ := store.(verify.Sink)
	if sink == nil {
		// 理论上不会发生：三种 sink 都提供计数。缺了就说明校验被接到了
		// 一个不认识的数据面上，宁可不做也不给假判定。
		c.logger.Warn("consistency check disabled: sink exposes no row counts", "source", string(c.sourceID))
		return nil
	}
	checkStore, _ := c.stateSvc.(state.CheckStore)
	return &checkRunner{
		checker: verify.Checker{
			Shaper: c.parser.(verify.Shaper),
			Sink:   sink,
			Opener: fileOpener{src: c.src, backupDir: c.backupDir},
		},
		store:   checkStore,
		emitCtx: c.emitCtx,
		logger:  c.logger,
	}
}

// VerifyNow 重新校验某一业务日的已完成文件（控制台手动触发）。判定与自动
// 链路同一套代码、同一张表，只是触发者与事件标记不同。
func (c *SourceUnitComponent) VerifyNow(ctx context.Context, key model.CollectionKey) ([]model.FileCheck, error) {
	c.mu.Lock()
	runner := c.active
	c.mu.Unlock()
	if runner == nil {
		return nil, errs.Sourcef(errs.ErrDependency, "数据源 %q 的行列一致性校验未启用（verify_after_collect=false，或本源解析器无法重测行列）", c.sourceID)
	}
	files, err := c.stateSvc.FileRecords(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errs.Sourcef(errs.ErrNotFound, "数据源 %q 在 %s 没有采集完成的文件", c.sourceID, key.Date.String())
	}
	checks := runner.run(ctx, key, files, "manual")
	if len(checks) == 0 {
		return nil, errs.Sourcef(errs.ErrNotFound, "数据源 %q 在 %s 的行列一致性校验没有产生判定", c.sourceID, key.Date.String())
	}
	return checks, nil
}

// VerificationActive 报告本源当前是否能做行列校验（控制台展示用）。
func (c *SourceUnitComponent) VerificationActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active != nil
}
