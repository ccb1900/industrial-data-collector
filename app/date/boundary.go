package date

import (
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// Boundary 是采集业务日的硬边界，只在 Handle 入口校验一次，内部不再重复判断。
//
// 口径只有一条：自动采集永远不越过 Cutoff，未来日期任何路径都不许。
//
// Cutoff 由 SameDay 决定。按天滚动的设备日志源是昨天——"今天"的文件还在写，
// 把它当作一个已完成的业务日采集，就是把半截文件当成全天的数据。仪表导出这类
// layout=flat 的源，文件本身就是当天的，SameDay 由 sourceunit 按 layout 设定，
// 不是运维可以随手打开的偏好。
//
// 手动指定日期（控制台、csv-collector）允许回到窗口之前：设备停机半年后，把
// 31 天以前的日期捞回来只有这一条通道，拒掉它等于用一条 P0 规则永久封死逃生口。
// 手动同样不许越过 Cutoff。
type Boundary struct {
	// Now 是时钟注入点（默认 time.Now）：边界只能靠注入时钟来测试月末、年末
	// 与闰年，不能靠等。
	Now func() time.Time
	// WindowDays 是自动补采窗口的天数（含 Cutoff 当天），与 Planner.CatchupDays
	// 读同一个配置项。0 = 不校验窗口下界（兼容未声明窗口的部署）。
	WindowDays int
	// SameDay 表示这个源的业务日就是当天（flat / 变更触发导出）。
	SameDay bool
}

func (b Boundary) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Today 是时钟意义上的今天（业务日粒度，无小时分量）。
func (b Boundary) Today() model.CollectionDate { return model.NewCollectionDate(b.now()) }

// Cutoff 是这个源允许采集的最新业务日。
func (b Boundary) Cutoff() model.CollectionDate {
	if b.SameDay {
		return b.Today()
	}
	return b.Today().AddDate(0, 0, -1)
}

// Start 返回自动补采窗口的首日（含）。WindowDays<=0 时没有下界。
// 公式与 recovery.Planner 共用同一个函数，窗口口径不许有两处算法。
func (b Boundary) Start() (model.CollectionDate, bool) {
	return WindowStart(b.Cutoff(), b.WindowDays)
}

// WindowStart 是窗口唯一的一处算法：days 天（含 upper 当天）的第一天。
func WindowStart(upper model.CollectionDate, days int) (model.CollectionDate, bool) {
	if days <= 0 {
		return model.CollectionDate{}, false
	}
	return upper.AddDate(0, 0, -(days - 1)), true
}

// Check 判定一个业务日能否进入采集。上界对自动与手动同一条：谁都不许越过
// Cutoff，更不许进未来。
//
// 没有"下界"这条规则，而且是刻意没有：窗口锚在 target 上（planner 排
// [target-(N-1), target]），所以 target 永远在自己窗口的顶端。date_policy =
// "specific" 指向一个旧日期是合法的自动回填入口，把它当越界拒绝，等于关掉
// 唯一一条按配置就能跑的补采通道——第一次实现时我加了这条规则，
// tests/ 里两个 specific 回填用例立刻被拒。
func (b Boundary) Check(target model.CollectionDate) error {
	if target.After(b.Today()) {
		return errs.Sourcef(errs.ErrDateBoundary,
			"业务日 %s 晚于今天 %s：未来的日期没有数据可采", target.String(), b.Today().String())
	}
	if target.After(b.Cutoff()) {
		return errs.Sourcef(errs.ErrDateBoundary,
			"业务日 %s 是今天，而该源按天采集昨天的数据（截止 %s）：自动任务绝不采集今天",
			target.String(), b.Cutoff().String())
	}
	return nil
}
