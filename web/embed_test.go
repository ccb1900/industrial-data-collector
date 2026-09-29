package web

import (
	"io/fs"
	"regexp"
	"testing"
)

func TestDistEmbeddedLayout(t *testing.T) {
	// go:embed all:dist stores files under a "dist/" prefix.
	if _, err := Dist.Open("dist/index.html"); err != nil {
		t.Fatalf("embedded dist/index.html: %v", err)
	}
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	// After fs.Sub the server can serve index.html at the root.
	if _, err := sub.Open("index.html"); err != nil {
		t.Fatalf("sub root index.html: %v", err)
	}
}

// TestDistAssetsResolve 拦住这类事故：index.html 被改成引用新的哈希产物，而产物
// 没入库（web/dist 曾被 .gitignore 的 dist/ 规则吞掉），构建出的二进制里 JS 404、
// 页面白屏，且 go test 全绿。
func TestDistAssetsResolve(t *testing.T) {
	b, err := fs.ReadFile(Dist, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`(?:src|href)="/((?:assets|[^"/]+)/[^"]+)"`).FindAllStringSubmatch(string(b), -1)
	if len(refs) == 0 {
		t.Fatal("index.html 引用不到任何资源，测试本身失效")
	}
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range refs {
		f, err := sub.Open(m[1])
		if err != nil {
			t.Errorf("index.html 引用 %q 在嵌入 FS 中不存在: %v", m[1], err)
			continue
		}
		f.Close()
	}
}
