package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
)

func TestCountPrunableMatchesPruneOne(t *testing.T) {
	root := t.TempDir()
	snaps := filepath.Join(root, "snapshots")
	for _, n := range []string{"2026-06-24_030000", "2026-06-23_030000", "2026-06-22_030000"} {
		if err := os.MkdirAll(filepath.Join(snaps, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := config.Sync{Name: "web", LocalPath: root}
	d := config.Defaults{Retention: config.Retention{Recent: 1}}
	deps := Deps{Runner: &execx.FakeRunner{}, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	n, err := CountPrunable(context.Background(), s, d, deps)
	if err != nil {
		t.Fatalf("count err: %v", err)
	}
	res := PruneOne(context.Background(), s, d, deps)
	if n != res.Pruned {
		t.Fatalf("CountPrunable = %d but PruneOne pruned %d", n, res.Pruned)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
}

func TestPruneOneDeletesExcess(t *testing.T) {
	root := t.TempDir()
	snaps := filepath.Join(root, "snapshots")
	for _, n := range []string{"2026-06-24_030000", "2026-06-23_030000", "2026-06-22_030000"} {
		if err := os.MkdirAll(filepath.Join(snaps, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := config.Sync{Name: "web", LocalPath: root}
	d := config.Defaults{Retention: config.Retention{Recent: 1}} // keep only newest
	deps := Deps{Runner: &execx.FakeRunner{}, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	res := PruneOne(context.Background(), s, d, deps)
	if !res.OK || res.Pruned != 2 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(snaps, "2026-06-24_030000")); err != nil {
		t.Fatal("newest should survive")
	}
	if _, err := os.Stat(filepath.Join(snaps, "2026-06-22_030000")); err == nil {
		t.Fatal("old snapshot should be pruned")
	}
}
