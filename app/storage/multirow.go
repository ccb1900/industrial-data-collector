// 多行语句构造：把"每行一次网络往返"的逐行写入折叠为每 chunk 一次。
// batch_size=1000 的批次在局域网 Oracle 上逐行就是 1000 次 RTT（0.3-1ms
// 的 RTT 即 0.3-1s/批次）——写库成为切真库后的最大单项开销。
//
// 幂等语义逐方言保留：VALUES 系（mysql/sqlite/postgres）带 INSERT IGNORE /
// ON CONFLICT DO NOTHING；oracle/sqlserver 保持 MERGE（NOT MATCHED 才插
// 入）——oracle 表上有主键约束，普通 INSERT 在部分提交后的文件重放会
// 撞 ORA-00001 永久失败，MERGE 语义不可替换，只能折叠不能降级。
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
		n = 500
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
		aliases := make([]string, 0, len(cols))
		for i := range cols {
			aliases = append(aliases, srcAlias(i))
		}
		return fmt.Sprintf("MERGE INTO %s dst USING (VALUES %s) src (%s) ON (%s) WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
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
