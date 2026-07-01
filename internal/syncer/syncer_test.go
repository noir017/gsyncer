package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// rsync exit 24 (vanished source files) is a warning, not a failure: the
// snapshot must still be taken and the entry reported OK.
func TestSyncOneRsyncExit24Continues(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 && args[0] == "--version" {
			return execx.Result{Stdout: "rsync version 3"}, nil
		}
		if name == "ssh" {
			return execx.Result{Stdout: "/usr/bin/rsync"}, nil
		}
		if name == "rsync" {
			return execx.Result{
				Stdout: "Number of regular files transferred: 3\nTotal transferred file size: 9 bytes\n",
				Stderr: "some files vanished before they could be transferred",
				Code:   24,
			}, errors.New("exit status 24")
		}
		if name == "cp" {
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
		t.Fatalf("exit 24 should be non-fatal, res = %+v", res)
	}
	if res.Files != 3 {
		t.Fatalf("stats not parsed on warning path: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(s.LocalPath, "snapshots", "2026-06-24_030000")); err != nil {
		t.Fatalf("snapshot not created on warning path: %v", err)
	}
}

// When another run already holds the per-root lock, SyncOne skips (does no
// work, reports Skipped) instead of running rsync --delete concurrently.
func TestSyncOneSkipsWhenLocked(t *testing.T) {
	s := okEntry(t)
	held, ok, err := acquireLock(s.LocalPath)
	if err != nil || !ok {
		t.Fatalf("could not take lock in test: ok=%v err=%v", ok, err)
	}
	defer held.release()

	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		return execx.Result{}, nil
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	res := SyncOne(context.Background(), s, config.Defaults{}, deps, false)
	if !res.Skipped || res.OK {
		t.Fatalf("expected Skipped and not OK, got %+v", res)
	}
	for _, c := range fr.Calls {
		t.Fatalf("no command should run while locked, got %+v", c)
	}
}

// A cancelled context stops SyncMany from launching entries; they are reported
// as Skipped (order preserved) rather than run against a dead context.
func TestSyncManyStopsOnCancelledContext(t *testing.T) {
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		return execx.Result{}, nil
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before any entry runs
	results := SyncMany(ctx, []config.Sync{okEntry(t), okEntry(t)}, config.Defaults{}, deps, false, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results (all skipped), got %d", len(results))
	}
	for _, r := range results {
		if !r.Skipped || r.OK {
			t.Fatalf("expected Skipped and not OK after cancel, got %+v", r)
		}
	}
	for _, c := range fr.Calls {
		if c.Name == "rsync" || c.Name == "ssh" {
			t.Fatalf("no command should run after cancel, got %+v", c)
		}
	}
}

// SyncMany writes results by index, so the returned order matches input order
// even when entries complete out of order under concurrency.
func TestSyncManyParallelStableOrder(t *testing.T) {
	var entries []config.Sync
	for i := 0; i < 8; i++ {
		e := okEntry(t)
		e.Name = string(rune('a' + i))
		entries = append(entries, e)
	}
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 && args[0] == "--version" {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 1\n"}, nil
		}
		if name == "cp" {
			_ = os.MkdirAll(args[2], 0o755)
		}
		return execx.Result{}, nil
	}}
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	results := SyncMany(context.Background(), entries, d, deps, false, 4)
	if len(results) != len(entries) {
		t.Fatalf("want %d results, got %d", len(entries), len(results))
	}
	for i, r := range results {
		if r.Name != entries[i].Name {
			t.Fatalf("result[%d].Name = %q, want %q", i, r.Name, entries[i].Name)
		}
		if !r.OK || r.Err != nil {
			t.Fatalf("result[%d] = %+v", i, r)
		}
	}
}

// SyncMany must never run more than `jobs` entries at once, and with jobs > 1 it
// must actually overlap them (proving it is not silently serial).
func TestSyncManyRespectsJobsLimit(t *testing.T) {
	var inflight, maxInflight atomic.Int32
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 && args[0] == "--version" {
			n := inflight.Add(1)
			for {
				m := maxInflight.Load()
				if n <= m || maxInflight.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond) // hold the slot so overlap is observable
			inflight.Add(-1)
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 1\n"}, nil
		}
		if name == "cp" {
			_ = os.MkdirAll(args[2], 0o755)
		}
		return execx.Result{}, nil
	}}
	var entries []config.Sync
	for i := 0; i < 6; i++ {
		e := okEntry(t)
		e.Name = "e" + string(rune('0'+i))
		entries = append(entries, e)
	}
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	SyncMany(context.Background(), entries, d, deps, false, 2)
	if got := maxInflight.Load(); got > 2 {
		t.Fatalf("max concurrency %d exceeded jobs limit 2", got)
	}
	if got := maxInflight.Load(); got < 2 {
		t.Fatalf("expected real overlap with jobs=2, max concurrency was only %d", got)
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
	results := SyncMany(context.Background(), []config.Sync{good, bad}, config.Defaults{}, deps, false, 2)
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

func TestSyncOneFallsBackWhenCurrentNotSubvolume(t *testing.T) {
	s := okEntry(t)
	// current exists as a plain dir -> btrfs `subvolume show` will "fail"
	if err := os.MkdirAll(filepath.Join(s.LocalPath, "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "ssh" {
			return execx.Result{}, nil
		}
		if name == "btrfs" && len(args) >= 1 && args[0] == "--version" {
			return execx.Result{}, nil // btrfs available -> Detect picks btrfs
		}
		if name == "btrfs" && len(args) >= 2 && args[1] == "show" {
			return execx.Result{Code: 1}, errors.New("not a subvolume") // -> ErrCurrentNotSubvolume
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 2\n"}, nil
		}
		if name == "cp" {
			_ = os.MkdirAll(args[2], 0o755)
			return execx.Result{}, nil
		}
		return execx.Result{}, nil
	}}
	btrfsFS := func(string) (int64, error) { return snapshot.BtrfsMagic, nil }
	deps := Deps{Runner: fr, FSType: btrfsFS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	// Recent:5 so the single snapshot is kept (empty policy would prune it away).
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("res = %+v", res)
	}
	if res.Mode != "hardlink" {
		t.Fatalf("expected fallback to hardlink, got mode %q", res.Mode)
	}
	if _, err := os.Stat(filepath.Join(s.LocalPath, "snapshots", "2026-06-24_030000")); err != nil {
		t.Fatalf("snapshot via hardlink fallback not created: %v", err)
	}
}

// When current IS a valid subvolume but `btrfs subvolume snapshot` fails (e.g.
// quota exhausted), SyncOne must fall back to a hardlink snapshot rather than
// abort with no backup for this run.
func TestSyncOneBtrfsCreateFallsBackToHardlink(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 && args[0] == "--version" {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "ssh" {
			return execx.Result{}, nil
		}
		if name == "btrfs" && len(args) >= 1 && args[0] == "--version" {
			return execx.Result{}, nil // btrfs available -> Detect picks btrfs
		}
		if name == "btrfs" && len(args) >= 2 && args[1] == "show" {
			return execx.Result{}, nil // current is a valid subvolume
		}
		if name == "btrfs" && len(args) >= 2 && args[1] == "snapshot" {
			return execx.Result{Code: 1}, errors.New("quota exceeded") // Create fails
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 2\n"}, nil
		}
		if name == "cp" {
			_ = os.MkdirAll(args[2], 0o755) // emulate hardlink fallback copy
			return execx.Result{}, nil
		}
		return execx.Result{}, nil
	}}
	// current must already exist as a subvolume so EnsureCurrent takes the
	// `subvolume show` (ok) path rather than trying to create one.
	if err := os.MkdirAll(filepath.Join(s.LocalPath, "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	btrfsFS := func(string) (int64, error) { return snapshot.BtrfsMagic, nil }
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	deps := Deps{Runner: fr, FSType: btrfsFS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("expected fallback success, res = %+v", res)
	}
	if res.Mode != "hardlink" {
		t.Fatalf("expected mode hardlink after btrfs Create failure, got %q", res.Mode)
	}
	if _, err := os.Stat(filepath.Join(s.LocalPath, "snapshots", "2026-06-24_030000")); err != nil {
		t.Fatalf("hardlink fallback snapshot not created: %v", err)
	}
}

// fmtSprintf wraps fmt.Sprintf so the helper above stays terse.
func fmtSprintf(f string, a []any) string { return fmtSprintfImpl(f, a) }
