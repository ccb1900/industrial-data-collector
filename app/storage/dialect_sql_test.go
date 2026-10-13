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

// 发号计数器行的播种语句：并发下"报错后吞掉"在 Postgres/Oracle 里会中止
// 事务，所以各家都必须用自家的 insert-if-missing。
func TestSeedCounterStmtShape(t *testing.T) {
	cols := []string{"name", "next"}
	ss := seedCounterStmt("sqlserver", "F_file_key_seq", cols)
	if !strings.Contains(ss, "@p1") || !strings.HasSuffix(ss, ";") {
		t.Errorf("sqlserver seed shape: %s", ss)
	}
	if !strings.Contains(ss, "ON (dst.name = src.c0)") {
		t.Errorf("seed must conflict on name: %s", ss)
	}
	ora := seedCounterStmt("oracle", "F_file_key_seq", cols)
	if !strings.Contains(ora, ":1") || !strings.Contains(ora, "FROM DUAL") || strings.HasSuffix(ora, ";") {
		t.Errorf("oracle seed shape: %s", ora)
	}
	if !strings.Contains(seedCounterStmt("sqlite", "F_file_key_seq", cols), "ON CONFLICT (name) DO NOTHING") {
		t.Error("sqlite seed must be ON CONFLICT DO NOTHING")
	}
	if !strings.Contains(seedCounterStmt("mysql", "F_file_key_seq", cols), "INSERT IGNORE") {
		t.Error("mysql seed must be INSERT IGNORE")
	}
}
