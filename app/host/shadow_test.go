package host

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"gocordis-csv-collector/app/fleetstore"
)

// 配置权威核查报告 V1-1 的处置：文件的四层表在账本非空时被遮蔽（死信），
// 权威保持不变，但死信必须响——文件指纹变化一次，告警恰好一次。

func shadowHost(t *testing.T) (*Host, *bytes.Buffer, *fleetstore.Store) {
	t.Helper()
	buf := &bytes.Buffer{}
	h := &Host{log: slog.New(slog.NewTextHandler(buf, nil))}
	s, err := fleetstore.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h.SetFleetStore(s, nil)
	return h, buf, s
}

func TestShadowedFileChangeWarnsOncePerChange(t *testing.T) {
	h, buf, s := shadowHost(t)
	file1 := []byte("[[machines]]\nno = \"m1\"\n")

	// 账本为空：文件权威，遮蔽无从谈起——首次指纹记录不告警。
	h.refreshSeedFingerprint(file1)
	if strings.Contains(buf.String(), "shadowed") {
		t.Fatalf("empty ledger must not warn: %s", buf.String())
	}

	// 建立账本（等价于控制台首次编辑）：权威移交给账本。
	if _, err := s.Save(fleetstore.FleetDoc{
		Defaults: map[string]any{"batch_size": int64(100)},
	}, 0, fleetstore.SourceConsole); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	h.refreshSeedFingerprint(file1)
	if strings.Contains(buf.String(), "shadowed") {
		t.Fatalf("unchanged file must not warn: %s", buf.String())
	}

	// 文件被手改：遮蔽告警恰好一次（指纹已随告警更新，同内容重放不重复告）。
	file2 := append([]byte(nil), file1...)
	file2 = append(file2, []byte("\n[[machines]]\nno = \"m2\"\n")...)
	h.refreshSeedFingerprint(file2)
	if !strings.Contains(buf.String(), "shadowed") {
		t.Fatalf("hand-edited file must warn once: %s", buf.String())
	}
	buf.Reset()
	h.refreshSeedFingerprint(file2)
	if strings.Contains(buf.String(), "shadowed") {
		t.Fatalf("same content replayed must not warn again: %s", buf.String())
	}
}

// TestShadowedFileChangeWarnRequiresLedger：账本为空时文件就是权威，永远不告。
func TestShadowedFileChangeWarnRequiresLedger(t *testing.T) {
	h, buf, _ := shadowHost(t)
	if _, err := h.FleetRevision(); err != nil {
		t.Fatal(err)
	}
	h.refreshSeedFingerprint([]byte("[[machines]]\nno = \"m1\"\n"))
	h.refreshSeedFingerprint([]byte("[[machines]]\nno = \"m2\"\n"))
	if strings.Contains(buf.String(), "shadowed") {
		t.Fatalf("file-authoritative deployments must never warn: %s", buf.String())
	}
}
