package recovery

import (
	"context"
	"fmt"
	"time"

	"gocordis-csv-collector/app/model"
)

// Planner builds the set of collection keys a trigger should attempt.
//
// 有两种规划方式，由 CatchupDays 决定，互不混用：
//
//	CatchupDays > 0（现网应当一律显式声明，30 个自然日的自动容错窗口）：
//	每天重新扫描整个窗口。窗口内每一个业务日，只要台账不是
//	Succeeded/Skipped、也不是一把还没过租约的 Running 锁，就排进计划——
//	台账里根本没有行的"空洞"同样排进来。从最后一次成功往后合成缺口的老
//	做法会永久漏掉这类空洞：11-11 文件当天没到（不留行），11-15 采成了，
//	起跳点就此越过 11-11，即使它后来补上也没人再看。
//	同时窗口是重试的上界：窗口外的失败记录停止自动重试，但一条都不删——
//	停机半年的设备恢复时，不受控的历史回灌本身就是事故。
//
//	CatchupDays == 0（兼容未声明窗口的旧部署）：保持原语义，已知未完成 +
//	(最后一次成功, target] 合成缺口，且已知未完成不受窗口约束。
type Planner struct {
	State model.CollectionState
	// Now 是时钟注入点（默认 time.Now）：窗口下界与 Running 租约都要读时钟，
	// 月末/年末/闰年与租度过期只能靠注入，不能靠等。
	Now func() time.Time
	// Lease 是一把 Running 锁的有效期：窗口内某天真有执行者在跑时，本轮跳过
	// 它，而不是把同一个日期排成第二次执行。零值取 24 小时（与改造前
	// Begin/ListIncomplete 的硬编码一致）。
	Lease time.Duration
	// CatchupDays 是自动补采窗口的天数（含 target 当天）。
	CatchupDays int
}

func (p *Planner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Planner) lease() time.Duration {
	if p.Lease > 0 {
		return p.Lease
	}
	return 24 * time.Hour
}

// windowStart 返回自动容错窗口的首日（含）。target 是本轮的业务日上界。
func (p *Planner) windowStart(target model.CollectionDate) model.CollectionDate {
	return target.AddDate(0, 0, -(p.CatchupDays - 1))
}

func (p *Planner) Plan(ctx context.Context, sourceID model.SourceID, target model.CollectionDate) ([]model.CollectionKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.CatchupDays > 0 {
		return p.planWindow(ctx, sourceID, target)
	}
	return p.planLegacy(ctx, sourceID, target)
}

// planWindow 逐日对齐窗口与台账：窗口内每个业务日要么已完成、要么进计划。
// 一次 CollectionRecords 读就是全部依据——规划口径不再散在 ListIncomplete 与
// 日历合成两处。
func (p *Planner) planWindow(ctx context.Context, sourceID model.SourceID, target model.CollectionDate) ([]model.CollectionKey, error) {
	records, err := p.State.CollectionRecords(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("collection records: %w", err)
	}
	byDate := make(map[model.CollectionDate]model.CollectionRecord, len(records))
	for _, rec := range records {
		if rec.Key.SourceID != sourceID {
			continue
		}
		byDate[rec.Key.Date] = rec
	}
	lease := p.lease()
	now := p.now()
	start := p.windowStart(target)
	var out []model.CollectionKey
	for d := start; !d.After(target); d = d.AddDate(0, 0, 1) {
		rec, existed := byDate[d]
		switch {
		case !existed:
			// 空洞：没有记录既不代表完成，也不代表无数据，必须重新探。
		case rec.Status == model.StatusSucceeded || rec.Status == model.StatusSkipped:
			continue
		case rec.Status == model.StatusRunning && now.Sub(rec.StartedAt) < lease:
			// 还在租约里的 Running 属于别人手上正在跑的活，本轮不重复执行。
			continue
		}
		out = append(out, model.CollectionKey{SourceID: sourceID, Date: d})
	}
	return out, nil
}

// planLegacy 是 CatchupDays == 0 时的原行为，保持不变。
func (p *Planner) planLegacy(ctx context.Context, sourceID model.SourceID, target model.CollectionDate) ([]model.CollectionKey, error) {
	known, err := p.State.ListIncomplete(ctx, sourceID, target, p.lease())
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
	// Synthesize the gap (last success, target].
	start := target
	planGap := false
	if ok {
		start = last.AddDate(0, 0, 1)
		planGap = !start.After(target)
	}
	if planGap {
		for d := start; !d.After(target); d = d.AddDate(0, 0, 1) {
			add(model.CollectionKey{SourceID: sourceID, Date: d})
		}
	}
	return ordered, nil
}
