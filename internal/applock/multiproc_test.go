package applock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	helperEnv        = "GOCORDIS_APLOCK_HELPER"
	helperDirEnv     = "GOCORDIS_APLOCK_DIR"
	helperOutEnv     = "GOCORDIS_APLOCK_OUT"
	helperReleaseEnv = "GOCORDIS_APLOCK_RELEASE_FLAG"
)

// TestMain turns the test binary into a lock helper when helperEnv is set.
// This gives the multi-process test real, independent OS processes rather
// than goroutines sharing the same file descriptor table.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		runLockHelper()
		return
	}
	os.Exit(m.Run())
}

func runLockHelper() {
	dir := os.Getenv(helperDirEnv)
	out := os.Getenv(helperOutEnv)
	releaseFlag := os.Getenv(helperReleaseEnv)
	write := func(s string) {
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(3)
		}
		if _, err := f.WriteString(s); err != nil {
			os.Exit(3)
		}
		_ = f.Close()
	}
	release, err := AcquireDir(dir)
	if err != nil {
		write("busy\n")
		os.Exit(0)
	}
	write("held\n")
	if releaseFlag != "" {
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat(releaseFlag); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(4)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	release()
	write("released\n")
}

func startLockHelper(t *testing.T, dir, out, releaseFlag string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		helperDirEnv+"="+dir,
		helperOutEnv+"="+out,
		helperReleaseEnv+"="+releaseFlag,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func waitForOutcome(t *testing.T, out string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(out); err == nil {
			s := string(data)
			if strings.HasSuffix(s, "\n") &&
				(strings.HasPrefix(s, "held\n") || strings.HasPrefix(s, "busy\n")) {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for helper outcome %s", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMultiProcessSingleWinner is the regression for both audited defects:
// exactly one process may hold the lock on the resolved state dir, and after
// it releases, a fresh process must be able to acquire despite the lock file
// still existing (never unlinked, so no stale-file race).
func TestMultiProcessSingleWinner(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	releaseFlag := filepath.Join(root, "release")
	const contenders = 4
	cmds := make([]*exec.Cmd, contenders)
	outs := make([]string, contenders)
	for i := range cmds {
		outs[i] = filepath.Join(root, fmt.Sprintf("out-%d.txt", i))
		cmds[i] = startLockHelper(t, dir, outs[i], releaseFlag)
	}

	winners := 0
	for _, out := range outs {
		result := waitForOutcome(t, out)
		switch {
		case strings.HasPrefix(result, "held\n"):
			winners++
		case strings.HasPrefix(result, "busy\n"):
		default:
			t.Fatalf("unexpected helper outcome %q in %s", result, out)
		}
	}
	if winners != 1 {
		t.Fatalf("want exactly 1 winner among %d processes, got %d", contenders, winners)
	}

	lock := filepath.Join(dir, ".lock")
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	if err := os.WriteFile(releaseFlag, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper %d exited with error: %v", i, err)
		}
	}

	after, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("lock file must remain after release: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lock file was replaced/unlinked across process handoff")
	}

	// A brand-new process acquires immediately with no stale-file race.
	nextOut := filepath.Join(root, "out-next.txt")
	next := startLockHelper(t, dir, nextOut, "")
	if err := next.Wait(); err != nil {
		t.Fatalf("new process failed to acquire after release: %v", err)
	}
	result, err := os.ReadFile(nextOut)
	if err != nil || !strings.HasPrefix(string(result), "held\n") {
		t.Fatalf("new process outcome = %q, err = %v", result, err)
	}
}
