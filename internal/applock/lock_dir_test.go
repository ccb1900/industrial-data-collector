package applock

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// testConfigFlag mimics the -config flag declared by both executables. Acquire
// uses it to find the anchored config file when called with the legacy
// relative "state" argument.
var testConfigFlag = flag.String("config", "", "config path (applock resolution test)")

func TestAcquireDirStableLockFile(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, ".lock")
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("lock file missing while held: %v", err)
	}
	if _, err := AcquireDir(dir); err == nil {
		t.Fatal("second AcquireDir must fail while held")
	}
	release()
	after, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("lock file must survive release: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lock file was unlinked/replaced on release — stale-file race is possible")
	}
	again, err := AcquireDir(dir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	again()
}

func TestAcquireUsesConfiguredStateDir(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "configs")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(cfgDir, "app.toml")
	// The audited layout: [defaults] state_dir points outside the config dir.
	if err := os.WriteFile(cfg, []byte("[defaults]\nstate_dir = \"../state\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := *testConfigFlag
	t.Cleanup(func() {
		*testConfigFlag = oldConfig
		_ = os.Chdir(cwd)
	})
	// AnchorConfigDir has already chdir'd into the config directory.
	if err := os.Chdir(cfgDir); err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(root, "state", ".lock")
	wrong := filepath.Join(cfgDir, "state", ".lock")
	// Both executables pass a -config value; it may be relative to the
	// pre-anchor CWD ("configs/app.toml") or absolute. AnchorConfigDir puts
	// the file in CWD in either case.
	for _, tc := range []struct {
		name, flagValue string
	}{
		{"basename", "app.toml"},
		{"relative with dir", filepath.Join("configs", "app.toml")},
		{"absolute", cfg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*testConfigFlag = tc.flagValue
			release, err := Acquire("state")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if _, err := os.Stat(resolved); err != nil {
				t.Fatalf("lock must cover configured state dir %s: %v", resolved, err)
			}
			if _, err := os.Stat(wrong); err == nil {
				t.Fatalf("lock must not cover <config dir>/state %s", wrong)
			}
		})
	}
}
