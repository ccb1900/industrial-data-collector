// 多行语句构造：把"每行一次网络往返"的逐行写入折叠为每 chunk 一次。
// batch_size=1000 的批次在局域网 Oracle 上逐行就是 1000 次 RTT（0.3-1ms
// 的 RTT 即 0.3-1s/批次）——写库成为切真库后的最大单项开销。
//
// 幂等语义逐方言保留：mysql 用 INSERT IGNORE，sqlite/postgres 用 ON CONFLICT
// DO NOTHING；oracle/sqlserver 的 fresh 键走无探测直插（新发的键不可能有
// 既有行），重放走反连接 INSERT ... WHERE NOT EXISTS 只补缺失行。MERGE 只剩
// 撞唯一键时的回退与注册表单行 insert-if-missing——它在数据行上的探测锁要
// 持有到提交，多文件并发时会成环，不能留在写路径里。
package storage

import (
	"fmt"
	"strings"
)

// chunkRowsFor 返回该方言单个语句的行数上限（受参数个数上限与 MERGE
// 语句文本膨胀约束）。方言值与 sourceunit/buildStorage 一致：
// "postgres"（不是 "postgresql"）——构造器分错了方言会把 VALUES 系
// 落进 MERGE 分支，这里与 buildMultiRow 共用同一套名字常量。
func chunkRowsFor(dialect string, colCount int) int {
	if colCount <= 0 {
		colCount = 1
	}
	n := 1000 // mysql 默认（参数上限另由 65535 封顶）
	switch dialect {
	case "oracle":
		n = 200 // MERGE ... UNION ALL 的语句文本随行数线性膨胀，保守取值
	case "sqlserver":
		// 实测（2019 CU32）：一条请求的绑定参数总数上限是硬限制——2098 通过、
		// 2100 报 8003，Prepare+Exec 与直接 Exec 同限。参数数 = 行数 × 列数，
		// 所以行数必须按列宽反算（20 列的生产格式只能 90 行），给固定行数
		// 会让每个超过阈值的文件永久失败。
		n = 2000 / colCount
	case "sqlite":
		n = 30000 / colCount // SQLITE_MAX_VARIABLE_NUMBER（3.32+ 默认 32766）
	case "postgres", "postgresql":
		n = 60000 / colCount // 65535 绑定参数上限，留余量
	}
	if cap := 65535 / colCount; n > cap { // 各方言预备语句的绑定参数硬上限
		n = cap
	}
	if n > 1000 {
		n = 1000
	}
	if n < 1 {
		n = 1
	}
	return n
}

// buildMultiRow 构造 n 行的写入语句。绑定参数行优先（row-major）顺序
// 编号：第 i 行第 j 列的参数序号 = i*len(cols)+j+1。
func buildMultiRow(dialect, table string, cols []string, conflict string, n int) string {
	q := quoteIdent(dialect, table)
	switch dialect {
	case "mysql":
		return fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES %s",
			q, strings.Join(cols, ", "), strings.Join(valueRows(dialect, cols, n), ", "))
	case "sqlite", "postgres", "postgresql":
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES %s ON CONFLICT (%s) DO NOTHING",
			q, strings.Join(cols, ", "), strings.Join(valueRows(dialect, cols, n), ", "), conflict)
	case "sqlserver":
		// MERGE ... USING (VALUES (...),(...)) src (c0,c1,...)：多行 MERGE。
		// T-SQL 的 MERGE 必须以分号结束（否则 10713）。
		aliases := make([]string, 0, len(cols))
		for i := range cols {
			aliases = append(aliases, srcAlias(i))
		}
		return fmt.Sprintf("MERGE INTO %s dst USING (VALUES %s) src (%s) ON (%s) WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s);",
			q, strings.Join(valueRows(dialect, cols, n), ", "),
			strings.Join(aliases, ", "),
			strings.Join(onClause(cols, conflict), " AND "),
			strings.Join(cols, ", "), strings.Join(srcRefs(len(cols)), ", "))
	default: // oracle
		// MERGE ... USING (SELECT :1 AS c0, ... FROM DUAL UNION ALL ...) src：
		// Oracle 无 VALUES 表构造器，用 UNION ALL 选择列表充当行集。
		sels := make([]string, 0, n)
		for i := 0; i < n; i++ {
			ph := aliasedPlaceholders(i*len(cols)+1, len(cols))
			cols2 := make([]string, 0, len(cols))
			for c := range cols {
				cols2 = append(cols2, fmt.Sprintf("%s AS %s", ph[c], srcAlias(c)))
			}
			sels = append(sels, fmt.Sprintf("SELECT %s FROM DUAL", strings.Join(cols2, ", ")))
		}
		return fmt.Sprintf("MERGE INTO %s dst USING (%s) src ON (%s) WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
			q, strings.Join(sels, " UNION ALL "),
			strings.Join(onClause(cols, conflict), " AND "),
			strings.Join(cols, ", "), strings.Join(srcRefs(len(cols)), ", "))
	}
}

// buildInsert 构造无冲突处理的多行 INSERT，只用于 fresh 键——新发的键在
// 数据表里不可能已有行。Oracle 不支持多行 VALUES，用 SELECT ... FROM DUAL
// UNION ALL 展开。撞唯一键只可能来自并发重复处理同一文件，调用方回退到
// 幂等语句。
func buildInsert(dialect, table string, cols []string, n int) string {
	q := quoteIdent(dialect, table)
	if dialect == "oracle" {
		sels := make([]string, 0, n)
		for i := 0; i < n; i++ {
			sels = append(sels, fmt.Sprintf("SELECT %s FROM DUAL", placeholders(dialect, i*len(cols)+1, len(cols))))
		}
		return fmt.Sprintf("INSERT INTO %s (%s) %s", q, strings.Join(cols, ", "), strings.Join(sels, " UNION ALL "))
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		q, strings.Join(cols, ", "), strings.Join(valueRows(dialect, cols, n), ", "))
}

// buildInsertMissing 构造"只插缺失行"的语句，用于 SQL Server / Oracle 的
// 重放与已登记文件。两条路径都把 MERGE 从数据写路径里拿掉：反连接的探测
// 不占用 MERGE 在目标行上的探测锁（SQL Server 的 U 锁、Oracle 的 MERGE
// 目标读），多文件并发重放时不会形成锁环。并发重复处理同一文件仍可能撞
// 唯一键，由调用方回退 MERGE 重放。
func buildInsertMissing(dialect, table string, cols []string, conflict string, n int) string {
	q := quoteIdent(dialect, table)
	aliases := make([]string, 0, len(cols))
	for i := range cols {
		aliases = append(aliases, srcAlias(i))
	}
	on := strings.Join(onClause(cols, conflict), " AND ")
	if dialect == "oracle" {
		sels := make([]string, 0, n)
		for i := 0; i < n; i++ {
			ph := aliasedPlaceholders(i*len(cols)+1, len(cols))
			parts := make([]string, 0, len(cols))
			for c := range cols {
				parts = append(parts, fmt.Sprintf("%s AS %s", ph[c], srcAlias(c)))
			}
			sels = append(sels, fmt.Sprintf("SELECT %s FROM DUAL", strings.Join(parts, ", ")))
		}
		return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM (%s) src WHERE NOT EXISTS (SELECT 1 FROM %s dst WHERE %s)",
			q, strings.Join(cols, ", "), strings.Join(srcRefs(len(cols)), ", "),
			strings.Join(sels, " UNION ALL "), q, on)
	}
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM (VALUES %s) src (%s) WHERE NOT EXISTS (SELECT 1 FROM %s dst WHERE %s);",
		q, strings.Join(cols, ", "), strings.Join(srcRefs(len(cols)), ", "),
		strings.Join(valueRows(dialect, cols, n), ", "),
		strings.Join(aliases, ", "), q, on)
}

func valueRows(dialect string, cols []string, n int) []string {
	rows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, "("+placeholders(dialect, i*len(cols)+1, len(cols))+")")
	}
	return rows
}

// aliasedPlaceholders 返回带序号占位符（oracle MERGE 的选择列表用）。
func aliasedPlaceholders(start, count int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, fmt.Sprintf(":%d", start+i))
	}
	return out
}

func srcAlias(i int) string { return fmt.Sprintf("c%d", i) }
func srcRefs(n int) []string {
	refs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		refs = append(refs, "src."+srcAlias(i))
	}
	return refs
}

func onClause(cols []string, conflict string) []string {
	keySet := map[string]bool{}
	for _, k := range strings.Split(conflict, ", ") {
		keySet[strings.ToLower(strings.TrimSpace(k))] = true
	}
	var on []string
	for i, c := range cols {
		if keySet[strings.ToLower(strings.Trim(c, `"`))] {
			on = append(on, fmt.Sprintf("dst.%s = src.%s", c, srcAlias(i)))
		}
	}
	return on
}
