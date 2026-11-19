package consolebridge

// 机台×日汇总（Iteration E）：把"机台×格式×业务日"的台账折成"机台×业务日"
// 的健康视图。它是只读派生——不写任何状态、不建任何新实体：
//
//	source_id = 机台-格式（fleet 生成），machine/format 身份由 fleet 显式下发
//	（components/sourceunit 的 MachineID/FormatID），聚合不靠拆前缀。
//
// 健康的定义：该机台该业务日的全部格式都已 Succeeded 或 Skipped（含确认
// 无数据）。Expired 单独成数——它不是"失败重试"，是"待人工回填"，混进
// failed 会把"去看一眼"和"必须有人行动"两种 ask 搅在一起。

import (
	"context"
	"net/url"
	"sort"
	"strconv"

	"dynamic-runtime/extensions/console/hub"

	"gocordis-csv-collector/app/model"
	sourceunitplugin "gocordis-csv-collector/components/sourceunit"
)

// MachineDayUnit 是一台源单元喂给聚合的输入：显式身份 + 该源的台账记录。
// ReadError 标记"台账读取失败"——这样的源不能从视图里消失，否则一次
// SQLite busy 会让机台日在缺了一个格式的情况下被算成健康。
type MachineDayUnit struct {
	MachineID string
	FormatID  string
	SourceID  string
	Records   []model.CollectionRecord
	ReadError bool
}

// MachineDayFilter 是查询参数（全部可选）。
type MachineDayFilter struct {
	Machine string // 按机台号过滤，空 = 全部
	Date    string // 按业务日过滤（YYYY-MM-DD），空 = 全部
	Limit   int    // 返回行数上限；<=0 = 默认 50
}

type machineDayFormat struct {
	SourceID string `json:"sourceId"`
	Format   string `json:"format"`
	Status   string `json:"status"`
	Note     string `json:"note,omitempty"`
}

type MachineDayRow struct {
	Machine   string `json:"machine"`
	Date      string `json:"date"`
	Healthy   bool   `json:"healthy"`
	Succeeded int    `json:"succeeded"`
	NoData    int    `json:"noData"`
	Failed    int    `json:"failed"`
	Pending   int    `json:"pending"`
	Expired   int    `json:"expired"`
	// ReadErrors 计数"台账读失败的源"。视图宁可承认看不见，也不把缺席
	// 当健康——缺证据的"健康"是最危险的那种。
	ReadErrors int                `json:"readErrors"`
	Formats    []machineDayFormat `json:"formats"`
}

// MachineDayRows 是本文件唯一的一处聚合算法（"一个决定只活在一个地方"）。
// 输入顺序无关；输出按机台升序、业务日降序（操作员先看最新的一天）。
// 机台身份缺失（手写 component）的源按 source_id 自成一组，不丢失。
func MachineDayRows(units []MachineDayUnit, f MachineDayFilter) []MachineDayRow {
	type key struct{ machine, date string }
	grouped := map[key][]machineDayFormat{}
	readErrorsByKey := map[key]int{}
	// 第一遍：正常台账按 (机台, 业务日) 分组，并记下每台机器已知的最新日期。
	latestDate := map[string]string{}
	for _, u := range units {
		if u.ReadError {
			continue
		}
		machine := u.MachineID
		if machine == "" {
			machine = u.SourceID
		}
		if f.Machine != "" && machine != f.Machine {
			continue
		}
		for _, rec := range u.Records {
			d := rec.Key.Date.String()
			if f.Date != "" && d != f.Date {
				continue
			}
			grouped[key{machine, d}] = append(grouped[key{machine, d}], machineDayFormat{
				SourceID: string(rec.Key.SourceID),
				Format:   u.FormatID,
				Status:   string(rec.Status),
				Note:     rec.Note,
			})
			if d > latestDate[machine] {
				latestDate[machine] = d
			}
		}
	}
	// 第二遍：台账读失败的源以 Unknown 入列并压制健康判定——绝不静默缺席。
	// 归属日期：有过滤用过滤日期；否则挂到该机台已知最新的一天（审查场景：
	// m1 的 readings 在 11-18 健康、alm_log 读失败 → 11-18 必须显示不健康，
	// 而不是让 alm_log 消失）；整台机器都读不到时落 ""（诚实承认日期未知）。
	for _, u := range units {
		if !u.ReadError {
			continue
		}
		machine := u.MachineID
		if machine == "" {
			machine = u.SourceID
		}
		if f.Machine != "" && machine != f.Machine {
			continue
		}
		d := f.Date
		if d == "" {
			d = latestDate[machine]
		}
		k := key{machine, d}
		grouped[k] = append(grouped[k], machineDayFormat{
			SourceID: u.SourceID, Format: u.FormatID,
			Status: "Unknown", Note: "ledger read failed",
		})
		readErrorsByKey[k]++
	}
	rows := make([]MachineDayRow, 0, len(grouped))
	for k, formats := range grouped {
		row := MachineDayRow{Machine: k.machine, Date: k.date, Formats: formats, ReadErrors: readErrorsByKey[k]}
		sort.Slice(formats, func(i, j int) bool { return formats[i].Format < formats[j].Format })
		for _, fm := range formats {
			switch model.Status(fm.Status) {
			case model.StatusSucceeded:
				row.Succeeded++
			case model.StatusSkipped:
				row.NoData++
			case model.StatusFailed:
				row.Failed++
			case model.StatusPending, model.StatusRunning:
				row.Pending++
			case model.StatusExpired:
				row.Expired++
			}
		}
		// Expired 不需要单列禁项：五个状态桶互斥且完备，
		// Succeeded+NoData == len(formats)-readErrors 已经排掉了
		// Expired/Failed/Pending。Unknown 条目（台账读失败）由 ReadErrors
		// 压制健康判定——缺证据的"健康"是最危险的那种。
		row.Healthy = row.Failed == 0 && row.Pending == 0 && row.ReadErrors == 0 &&
			row.Succeeded+row.NoData == len(formats)-row.ReadErrors
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Machine != rows[j].Machine {
			return rows[i].Machine < rows[j].Machine
		}
		return rows[i].Date > rows[j].Date // 最新的一天排前面
	})
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// machineDayParams 解析查询参数；非法 limit 报 invalid_request（与既有一致）。
func machineDayParams(params url.Values) (MachineDayFilter, *hub.Error) {
	f := MachineDayFilter{
		Machine: params.Get("machine"),
		Date:    params.Get("date"),
	}
	if v := params.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, &hub.Error{Code: "invalid_request", Message: "limit 必须是非负整数：" + v}
		}
		f.Limit = n
	}
	return f, nil
}

// machineDayRegistrar 只依赖聚合真正需要的那一小面：注册一个命名查询。
// 框架的注册器类型不变时它天然满足；测试可以用内存替身。
type machineDayRegistrar interface {
	RegisterQuery(name string, owner string, handler hub.QueryHandler) (func() error, error)
}

// registerMachineDays 把"机台×日"汇总挂成 hub 查询，与其它命名查询同批注册。
// 读台账时快照 units：reconcile 期间 SetUnits 换集合不会读到半新半旧的切片。
func (c *Component) registerMachineDays(reg machineDayRegistrar, owner string) (func() error, error) {
	return reg.RegisterQuery("machine-days", owner, hub.QueryHandler(func(ctx context.Context, params url.Values) (any, *hub.Error) {
		f, perr := machineDayParams(params)
		if perr != nil {
			return nil, perr
		}
		return MachineDayRows(c.machineDayUnits(ctx), f), nil
	}))
}

// machineDayUnits 把源单元集合折成聚合输入。单源读台账失败不拖垮整个视图：
// 该源缺席即缺席——这本身就是"这台机器这一天不健康"的证据。
func (c *Component) machineDayUnits(ctx context.Context) []MachineDayUnit {
	c.mu.Lock()
	units := append([]*sourceunitplugin.SourceUnitComponent(nil), c.units...)
	c.mu.Unlock()
	out := make([]MachineDayUnit, 0, len(units))
	for _, u := range units {
		if u == nil {
			continue
		}
		recs, err := u.Records(ctx)
		if err != nil {
			continue
		}
		out = append(out, MachineDayUnit{
			MachineID: u.MachineID(),
			FormatID:  u.FormatID(),
			SourceID:  u.SourceID(),
			Records:   recs,
		})
	}
	return out
}
