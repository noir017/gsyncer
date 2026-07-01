package restore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gsync/internal/execx"
)

func mkTimes() []time.Time {
	return []time.Time{
		time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC), // newest, out of order on purpose
		time.Date(2026, 6, 23, 3, 0, 0, 0, time.UTC),
	}
}

func TestSelectTimeLatest(t *testing.T) {
	got, err := SelectTime(mkTimes(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format("2006-01-02_150405") != "2026-06-24_030000" {
		t.Fatalf("latest = %v", got)
	}
}

func TestSelectTimeAtExact(t *testing.T) {
	got, err := SelectTime(mkTimes(), "2026-06-23_030000", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day() != 23 {
		t.Fatalf("at = %v", got)
	}
}

func TestSelectTimeAtNotFound(t *testing.T) {
	if _, err := SelectTime(mkTimes(), "2020-01-01_000000", false); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestSelectTimeBadFormat(t *testing.T) {
	if _, err := SelectTime(mkTimes(), "nonsense", false); err == nil {
		t.Fatal("expected format error")
	}
}

func TestSelectTimeEmpty(t *testing.T) {
	if _, err := SelectTime(nil, "", true); err == nil {
		t.Fatal("expected empty error")
	}
}

// cpFake creates the destination dir so downstream stat checks see a result.
func cpFake() *execx.FakeRunner {
	return &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "cp" {
			_ = os.MkdirAll(args[len(args)-1], 0o755)
		}
		return execx.Result{}, nil
	}}
}

func TestRunRefusesCurrentDir(t *testing.T) {
	local := t.TempDir()
	dst := filepath.Join(local, "current")
	fr := cpFake()
	if err := Run(context.Background(), fr, local, "/snap", dst, true); err == nil {
		t.Fatal("expected refusal to overwrite current/")
	}
	for _, c := range fr.Calls {
		if c.Name == "cp" {
			t.Fatal("no cp should run when refusing current/")
		}
	}
}

// A relative --to that resolves to the entry's current/ (e.g. run from inside
// localPath as `--to current --force`) must still hit the guard, not delete it.
func TestRunRefusesRelativeCurrentDir(t *testing.T) {
	local := t.TempDir()
	cur := filepath.Join(local, "current")
	if err := os.MkdirAll(cur, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(cur, "live.txt")
	if err := os.WriteFile(marker, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(local); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	fr := cpFake()
	if err := Run(context.Background(), fr, local, "/snap", "current", true); err == nil {
		t.Fatal("expected refusal for relative --to current")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("live current/ must not be deleted: %v", err)
	}
}

func TestRunRefusesExistingWithoutForce(t *testing.T) {
	local := t.TempDir()
	dst := filepath.Join(local, "dest")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	fr := cpFake()
	if err := Run(context.Background(), fr, local, "/snap", dst, false); err == nil {
		t.Fatal("expected error for existing dst without --force")
	}
}

func TestRunForceClearsThenCopies(t *testing.T) {
	local := t.TempDir()
	dst := filepath.Join(local, "dest")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dst, "stale.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := restoreSnap(t, local)
	fr := cpFake()
	if err := Run(context.Background(), fr, local, snap, dst, true); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("stale file should be removed before copy")
	}
	var sawCp bool
	for _, c := range fr.Calls {
		if c.Name == "cp" && len(c.Args) == 3 && c.Args[0] == "-a" && c.Args[1] == snap && c.Args[2] == dst {
			sawCp = true
		}
	}
	if !sawCp {
		t.Fatalf("cp -a %s %s not invoked: %+v", snap, dst, fr.Calls)
	}
}

func TestRunFreshDestCopiesDirectly(t *testing.T) {
	local := t.TempDir()
	dst := filepath.Join(local, "newdest")
	snap := restoreSnap(t, local)
	fr := cpFake()
	if err := Run(context.Background(), fr, local, snap, dst, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dest not created: %v", err)
	}
}

// restoreSnap makes a fake snapshot dir and returns its path.
func restoreSnap(t *testing.T, local string) string {
	t.Helper()
	p := SnapPath(local, time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC))
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
