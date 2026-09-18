package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnchorConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(t.TempDir()) // 任意启动 CWD，测试结束后恢复

	configPath := filepath.Join(dir, "configs", "laser.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("bundles = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 相对路径形式的 -config 参数（终端/双击/计划任务传入的形态各异，
	// 锚定后行为一致）。
	rel, err := filepath.Rel(mustCWD(t), configPath)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := AnchorConfigDir(rel)
	if err != nil {
		t.Fatal(err)
	}
	if abs != configPath {
		t.Fatalf("abs = %q, want %q", abs, configPath)
	}
	// CWD 已锚定到配置文件所在目录：state/、path、plugins 等相对路径自此
	// 全部相对配置文件解析。（macOS 的临时目录在 /var→/private/var
	// 符号链接下，比较前统一解析。）
	wd := mustCWD(t)
	if real, err := filepath.EvalSymlinks(wd); err == nil {
		wd = real
	}
	want := filepath.Dir(configPath)
	if real, err := filepath.EvalSymlinks(want); err == nil {
		want = real
	}
	if wd != want {
		t.Fatalf("cwd = %q, want %q", wd, want)
	}
}

func mustCWD(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
