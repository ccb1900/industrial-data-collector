package storage

import (
	"strings"
	"testing"

	"gocordis-csv-collector/app/model"
)

// 方言 SQL 文本的离线回归：这些写法只有连上真库才会暴露（mssql 报 102/
// 10713/1946），但错误都在"生成语句"这一步，所以可以无库断言。真库那半边
// 由 sqlserver_real_test.go 在设置 REAL_SQLSERVER_DSN 时补测。

func plainQual(name string) string { return name }
func plainDate(s string) any       { return s }

// 冲突写入（注册表、计数器行）的各家写法。
func TestInsertIfMissingStmtPerDialect(t *testing.T) {
	cols := []string{"source_id", "collection_date", "file_id"}
	conflict := "source_id, collection_date"
	cases := []struct {
		dialect string
		must    []string
		mustNot []string
	}{
		// sqlserver：绑定符必须是 @pN（写成 :N 实测报 "Incorrect syntax near ':'"），
		// 且 MERGE 必须以分号结束（实测报 10713）。
		{"sqlserver", []string{"MERGE INTO", "@p1 AS c0", "@p3 AS c2", ";"}, []string{":1", "FROM DUAL", "ON CONFLICT"}},
		// oracle：绑定符 :N、源集必须带 FROM DUAL，且不能有多余分号。
		{"oracle", []string{"MERGE INTO", ":1 AS c0", ":3 AS c2", "FROM DUAL"}, []string{"@p1", "ON CONFLICT"}},
		// mysql：无 ON CONFLICT，幂等靠 INSERT IGNORE。
		{"mysql", []string{"INSERT IGNORE INTO"}, []string{"ON CONFLICT", "MERGE"}},
		// postgres：$N + ON CONFLICT。
		{"postgres", []string{"ON CONFLICT (source_id, collection_date) DO NOTHING", "$1", "$3"}, []string{"INSERT IGNORE", "MERGE"}},
		// sqlite：? + ON CONFLICT。
		{"sqlite", []string{"ON CONFLICT (source_id, collection_date) DO NOTHING", "?"}, []string{"MERGE", "$1"}},
	}
	for _, tc := range cases {
		stmt := insertIfMissingStmt(tc.dialect, "F", cols, conflict)
		for _, want := range tc.must {
			if !strings.Contains(stmt, want) {
				t.Errorf("%s: missing %q in %s", tc.dialect, want, stmt)
			}
		}
		for _, ban := range tc.mustNot {
			if strings.Contains(stmt, ban) {
				t.Errorf("%s: must not contain %q in %s", tc.dialect, ban, stmt)
			}
		}
	}
}

// 多行幂等写入（数据行）的 sqlserver 分支同样要收尾分号。
func TestBuildMultiRowSQLServerTerminatesMerge(t *testing.T) {
	cols := []string{"file_key", "row_number", `"temp"`}
	conflict := "file_key, row_number"
	stmt := buildMultiRow("sqlserver", "T", cols, conflict, 2)
	if !strings.HasSuffix(stmt, ";") {
		t.Errorf("sqlserver multi-row MERGE must end with ';', got %s", stmt)
	}
	if strings.Contains(stmt, ":1") || !strings.Contains(stmt, "@p4") {
		t.Errorf("sqlserver binds must be @pN: %s", stmt)
	}
	// oracle 的多行 MERGE 用 UNION ALL 选择列表，且不能带分号。
	ora := buildMultiRow("oracle", "T", cols, conflict, 2)
	if strings.HasSuffix(ora, ";") || !strings.Contains(ora, "UNION ALL") {
		t.Errorf("oracle multi-row MERGE shape wrong: %s", ora)
	}
}

// 读侧过滤条件的占位符编号必须随参数序递增，且按方言成形。
func TestReadFilterClausesPerDialect(t *testing.T) {
	declared := map[string]bool{"tag": true, "path": true}
	filters := map[string]string{"path": "/logs/a", "tag": "t1"}
	cases := []struct {
		dialect   string
		want      []string
		wantArgs  int
		wantNoSub []string
	}{
		{"sqlserver", []string{"source_id = @p1", "collection_date = @p2", "= @p3", "= @p4"}, 4, []string{"?"}},
		{"postgres", []string{"source_id = $1", "collection_date = $2", "= $3", "= $4"}, 4, nil},
		{"oracle", []string{"source_id = :1", "collection_date = :2", "= :3", "= :4"}, 4, nil},
		{"sqlite", []string{"source_id = ?", "collection_date = ?", "= ?", "= ?"}, 4, nil},
	}
	for _, tc := range cases {
		where, args, err := readFilterClauses(tc.dialect, plainQual, plainDate, "s1", "2026-09-09", filters, declared)
		if err != nil {
			t.Fatalf("%s: %v", tc.dialect, err)
		}
		if len(args) != tc.wantArgs {
			t.Errorf("%s: args = %d, want %d", tc.dialect, len(args), tc.wantArgs)
		}
		joined := strings.Join(where, " AND ")
		for _, want := range tc.want {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: clause %q missing in %s", tc.dialect, want, joined)
			}
		}
		for _, ban := range tc.wantNoSub {
			if strings.Contains(joined, ban) {
				t.Errorf("%s: clause must not contain %q: %s", tc.dialect, ban, joined)
			}
		}
	}
	// 过滤列必须是声明列：未声明的列名不进 SQL（防注入面）。
	if _, _, err := readFilterClauses("sqlite", plainQual, plainDate, "", "", map[string]string{"evil": "1"}, declared); err == nil {
		t.Error("undeclared filter column must fail")
	}
	// 留空维度不过滤（控制台跨批次查询依赖）。
	where, args, err := readFilterClauses("sqlserver", plainQual, plainDate, "", "", nil, declared)
	if err != nil {
		t.Fatal(err)
	}
	if len(where) != 0 || len(args) != 0 {
		t.Errorf("empty filters must yield no clauses, got %v / %v", where, args)
	}
}

// oracle 的 DATE 列：读写两侧的日期绑定必须同形。
func TestDateFilterBindMatchesDateBind(t *testing.T) {
	var d model.CollectionDate
	if err := d.UnmarshalText([]byte("2026-09-09")); err != nil {
		t.Fatal(err)
	}
	st := &TableStorage{cfg: TableConfig{Dialect: "oracle"}}
	bound := st.dateBind(d)
	filter := st.dateFilterBind("2026-09-09")
	if _, ok := bound.(string); ok {
		t.Fatal("oracle dateBind must not bind a string")
	}
	if _, ok := filter.(string); ok {
		t.Errorf("oracle filter must bind the same shape as writes, got string %v", filter)
	}
	sqlst := &TableStorage{cfg: TableConfig{Dialect: "sqlserver"}}
	if got := sqlst.dateFilterBind("2026-09-09"); got != "2026-09-09" {
		t.Errorf("sqlserver keeps the ISO string, got %v", got)
	}
	// 非 ISO 的日期串不猜：原样交给数据库。
	if got := st.dateFilterBind("昨天"); got != "昨天" {
		t.Errorf("unparseable date must pass through, got %v", got)
	}
}

// 注册表登记用的 insert-if-missing：并发下"报错后吞掉"在 Postgres/Oracle
// 里会中止事务，所以各家都必须用自家的写法。
func TestInsertIfMissingStmtShape(t *testing.T) {
	cols := []string{"file_key", "source_id", "collection_date", "file_id"}
	conflict := "source_id, collection_date, file_id"
	ss := insertIfMissingStmt("sqlserver", "F", cols, conflict)
	if !strings.Contains(ss, "@p1") || !strings.HasSuffix(ss, ";") {
		t.Errorf("sqlserver insert shape: %s", ss)
	}
	if !strings.Contains(ss, "ON (dst.source_id = src.c1 AND dst.collection_date = src.c2 AND dst.file_id = src.c3)") {
		t.Errorf("insert must conflict on the file triple: %s", ss)
	}
	ora := insertIfMissingStmt("oracle", "F", cols, conflict)
	if !strings.Contains(ora, ":1") || !strings.Contains(ora, "FROM DUAL") || strings.HasSuffix(ora, ";") {
		t.Errorf("oracle insert shape: %s", ora)
	}
	if !strings.Contains(insertIfMissingStmt("sqlite", "F", cols, conflict), "ON CONFLICT (source_id, collection_date, file_id) DO NOTHING") {
		t.Error("sqlite insert must be ON CONFLICT DO NOTHING")
	}
	if !strings.Contains(insertIfMissingStmt("mysql", "F", cols, conflict), "INSERT IGNORE") {
		t.Error("mysql insert must be INSERT IGNORE")
	}
}

// 单语句参数预算：chunkRowsFor 返回的行数乘以每行参数数（声明列 + 标识列）
// 必须留在服务端的绑定参数上限内。SQL Server 的上限是实测值——2098 通过、
// 2100 报 8003（Prepare+Exec 与直接 Exec 同限），这一条正是现场宽格式全队
// 失败的原因：分支里曾写死 500 行，20 列格式一语句 11000 参数。
// oracle 没有实测过的参数上限数字，只断言行数被保守封顶。
func TestChunkRowsForParamBudget(t *testing.T) {
	ceilings := map[string]int{
		"sqlserver": 2099,  // 实测
		"sqlite":    32766, // SQLITE_MAX_VARIABLE_NUMBER（3.32+ 默认）
		"postgres":  65535, // 协议绑定参数上限
		"mysql":     65535, // 预处理语句占位符上限
	}
	widths := []int{1, 2, 3, 5, 13, 22, 27, 47, 60, 200, 300}
	for dialect, ceiling := range ceilings {
		for _, cols := range widths {
			rows := chunkRowsFor(dialect, cols)
			if params := rows * cols; params > ceiling {
				t.Errorf("%s cols=%d rows=%d -> %d params > ceiling %d", dialect, cols, rows, params, ceiling)
			}
		}
	}
	for _, cols := range widths {
		if rows := chunkRowsFor("oracle", cols); rows > 200 {
			t.Errorf("oracle cols=%d rows=%d must stay at the conservative MERGE cap", cols, rows)
		}
	}
	if got := chunkRowsFor("sqlserver", 22); got != 90 {
		t.Errorf("sqlserver 22-col chunk = %d rows, want 90 (1980 params)", got)
	}
}
