package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/snapshot"
)

type captureLog struct{ lines []string }

func (c *captureLog) Infof(f string, a ...any)  { c.lines = append(c.lines, "I:"+sprintf(f, a)) }
func (c *captureLog) Errorf(f string, a ...any) { c.lines = append(c.lines, "E:"+sprintf(f, a)) }
func sprintf(f string, a []any) string          { return strings.TrimSpace(fmtSprintf(f, a)) }

func ext4FS(string) (int64, error) { return 0xEF53, nil }

func okEntry(t *testing.T) config.Sync {
	t.Helper()
	return config.Sync{
		Name: "web", Host: "h", User: "u", Identity: "",
		RemotePath: "/src", LocalPath: t.TempDir(),
	}
}

// happy path: rsync ok, hardlink snapshot created, prune keeps the only one.
func TestSyncOneHappyPath(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 && args[0] == "--version" {
			return execx.Result{Stdout: "rsync version 3"}, nil
		}
		if name == "ssh" {
			return execx.Result{Stdout: "/usr/bin/rsync"}, nil
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 5\nTotal transferred file size: 42 bytes\n"}, nil
		}
		if name == "cp" {
			// emulate cp -al by creating the dir so List() sees it
			_ = os.MkdirAll(args[2], 0o755)
			return execx.Result{}, nil
		}
		return execx.Result{}, nil
	}}
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("res = %+v", res)
	}
	if res.Files != 5 || res.Bytes != 42 {
		t.Fatalf("stats = %+v", res)
	}
	if res.Mode != "hardlink" {
		t.Fatalf("mode = %q", res.Mode)
	}
	if _, err := os.Stat(filepath.Join(s.LocalPath, "snapshots", "2026-06-24_030000")); err != nil {
		t.Fatalf("snapshot not created: %v", err)
	}
}

// rsync missing locally -> fail, no snapshot.
func TestSyncOneLocalRsyncMissing(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 {
			return execx.Result{Code: 127}, errors.New("not found")
		}
		return execx.Result{}, nil
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	res := SyncOne(context.Background(), s, config.Defaults{}, deps, false)
	if res.OK || res.Err == nil {
		t.Fatalf("expected failure, got %+v", res)
	}
}

// dry-run: no snapshot creation.
func TestSyncOneDryRun(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "ssh" {
			return execx.Result{}, nil
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 1\n"}, nil
		}
		if name == "cp" {
			t.Fatal("dry-run must not snapshot")
		}
		return execx.Result{}, nil
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	res := SyncOne(context.Background(), s, config.Defaults{}, deps, true)
	if !res.OK {
		t.Fatalf("dry-run res = %+v", res)
	}
}

func TestSyncManyIsolatesFailures(t *testing.T) {
	good := okEntry(t)
	good.Name = "good"
	bad := okEntry(t)
	bad.Name = "bad"
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "ssh" {
			return execx.Result{}, nil
		}
		if name == "rsync" && containsStr(args, "h-bad:/src/") {
			return execx.Result{Code: 1}, errors.New("rsync failed")
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 1\n"}, nil
		}
		if name == "cp" {
			_ = os.MkdirAll(args[2], 0o755)
		}
		return execx.Result{}, nil
	}}
	bad.Host = "h-bad"
	good.Host = "h-good"
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	results := SyncMany(context.Background(), []config.Sync{good, bad}, config.Defaults{}, deps, false)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	okCount := 0
	for _, r := range results {
		if r.OK {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("want exactly 1 ok, got %d: %+v", okCount, results)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// fmtSprintf wraps fmt.Sprintf so the helper above stays terse.
func fmtSprintf(f string, a []any) string { return fmtSprintfImpl(f, a) }

// snapshot.FSTypeFunc is used in test but not directly imported; suppress unused import.
var _ snapshot.FSTypeFunc = ext4FS
