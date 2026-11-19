package storage

import (
	"strings"
	"testing"
)

// INSERT IGNORE 是唯一一种"把任意错误降级成 warning"的幂等写法：严格模式下
// 字符串截断、NOT NULL 填默认值、类型溢出……全部变成 warning，行被静默改写或
// 丢弃，而采集器把整批当成功——数据错了都不知道错。其余幂等形态
// （ON CONFLICT DO NOTHING、反连接 WHERE NOT EXISTS、MERGE）都只在撞唯一键时
// 让步，其它错误照常上报。
//
// 这条门禁扫全部方言 × 全部构造路径（多行幂等、反连接重放、fresh 直插、通用
// JSON sink 的单行 upsert），保证吞错误的写法不会从任何一条路径回到代码里。
// 2026-11-20 之前 mysql 的三条路径全是 INSERT IGNORE。
func TestNoBuilderEmitsAnErrorSwallowingInsert(t *testing.T) {
	dialects := []string{"mysql", "postgres", "sqlite", "sqlserver", "oracle"}
	cols := []string{"file_key", "row_number", "v"}
	conflict := "file_key, row_number"
	for _, d := range dialects {
		stmts := map[string]string{
			"multiRow":          buildMultiRow(d, "t", cols, conflict, 3),
			"insertMissing":     buildInsertMissing(d, "t", cols, conflict, 3),
			"plainInsert":       buildInsert(d, "t", cols, 3),
			"genericUpsert":     mustUpsertSQL(t, d),
			"registryIfMissing": insertIfMissingStmt(d, "t", cols, conflict),
		}
		for name, stmt := range stmts {
			if strings.Contains(stmt, "INSERT IGNORE") {
				t.Errorf("%s/%s emits INSERT IGNORE, which downgrades every error to a warning: %s", d, name, stmt)
			}
		}
	}
}

func mustUpsertSQL(t *testing.T, dialect string) string {
	t.Helper()
	s := &SQLStore{dialect: dialect, table: "t"}
	stmt, err := s.upsertSQL(7)
	if err != nil {
		t.Fatalf("%s upsertSQL: %v", dialect, err)
	}
	return stmt
}

// TestMySQLIdempotentPathsAreAntiJoin 钉住 mysql 幂等的具体形状：与 mssql/oracle
// 同构的反连接（只补缺失行），绑定符 ?，绝无 MERGE、绝无 IGNORE。
// 真库回归：仓库里没有 MySQL 实例，真实语义由 TestNoBuilderEmitsAnErrorSwallowingInsert
// 的构造断言 + 现场 DSN（REAL_MYSQL_DSN，尚未有）验证；oracle 同构语句已有
// 形状断言。
func TestMySQLIdempotentPathsAreAntiJoin(t *testing.T) {
	cols := []string{"file_key", "row_number", "`v`"}
	conflict := "file_key, row_number"

	replay := buildInsertMissing("mysql", "t", cols, conflict, 2)
	for _, want := range []string{
		"INSERT INTO `t` (file_key, row_number, `v`) SELECT src.c0, src.c1, src.c2 FROM (",
		"SELECT ? AS c0, ? AS c1, ? AS c2 FROM DUAL UNION ALL SELECT ? AS c0, ? AS c1, ? AS c2 FROM DUAL",
		"WHERE NOT EXISTS (SELECT 1 FROM `t` dst WHERE dst.file_key = src.c0 AND dst.row_number = src.c1)",
	} {
		if !strings.Contains(replay, want) {
			t.Fatalf("mysql replay missing %q: %s", want, replay)
		}
	}
	if strings.Contains(replay, "MERGE") || strings.Contains(replay, "IGNORE") || strings.Contains(replay, "@p") || strings.Contains(replay, "$") {
		t.Fatalf("mysql replay must be plain anti-join with ? binds: %s", replay)
	}

	upsert := mustUpsertSQL(t, "mysql")
	if !strings.Contains(upsert, "WHERE NOT EXISTS") || strings.Contains(upsert, "IGNORE") {
		t.Fatalf("mysql generic upsert must be anti-join: %s", upsert)
	}
}
