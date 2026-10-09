package storage

import (
	"strings"
	"testing"
)

// 审查确认：mergeUpsert 在 file_key 注册表形状（9 列，file_key 为首列且
// 不是 ON 键）下的参数序与 ON 子句。resolveFileKey 的 oracle/sqlserver
// 路径依赖这一形状：绑定参数 file_key 在前与 c0 对齐，MERGE 命中既有行
// 时读回的是注册表里真正的键而不是本次误发的键。
func TestMergeUpsertRegistryShape(t *testing.T) {
	cols := []string{"file_key", "source_id", "collection_date", "file_id", "path", "name", "records", "header", "collected_at"}
	stmt := mergeUpsert("oracle", "F", cols, "source_id, collection_date, file_id")
	if !strings.Contains(stmt, ":1 AS c0") {
		t.Fatalf("first bind must be c0 (file_key): %s", stmt)
	}
	if !strings.Contains(stmt, "dst.source_id = src.c1 AND dst.collection_date = src.c2 AND dst.file_id = src.c3") {
		t.Fatalf("ON keys must be the identity triple: %s", stmt)
	}
	if strings.Contains(stmt, "dst.file_key = src.c0") {
		t.Fatalf("file_key must not be an ON key: %s", stmt)
	}
}
