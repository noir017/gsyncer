package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gsync/internal/execx"
)

func TestBtrfsCreateInvokesSubvolumeSnapshot(t *testing.T) {
	root := t.TempDir()
	fr := &execx.FakeRunner{}
	be := NewBtrfs(fr)
	ts := time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC)
	snap, err := be.Create(context.Background(), root, ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if snap != want {
		t.Fatalf("snap = %q", snap)
	}
	c := fr.Calls[0]
	if c.Name != "btrfs" || strings.Join(c.Args, " ") != "subvolume snapshot -r "+
		filepath.Join(root, "current")+" "+want {
		t.Fatalf("btrfs args = %v", c.Args)
	}
}

func TestBtrfsCreateAvoidsCollision(t *testing.T) {
	root := t.TempDir()
	ts := time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC)
	// Simulate a snapshot subvolume already present for the same second.
	taken := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if err := os.MkdirAll(taken, 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewBtrfs(&execx.FakeRunner{})
	snap, err := be.Create(context.Background(), root, ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030001")
	if snap != want {
		t.Fatalf("snap = %q, want %q (should bump to avoid collision)", snap, want)
	}
}

func TestBtrfsEnsureCurrentNotSubvolume(t *testing.T) {
	root := t.TempDir()
	// pre-create current as a plain dir
	be := NewHardlink(&execx.FakeRunner{})
	if _, err := be.EnsureCurrent(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		// `btrfs subvolume show` fails -> not a subvolume
		return execx.Result{Code: 1}, errors.New("not a subvolume")
	}}
	bb := NewBtrfs(fr)
	if _, err := bb.EnsureCurrent(context.Background(), root); !errors.Is(err, ErrCurrentNotSubvolume) {
		t.Fatalf("err = %v, want ErrCurrentNotSubvolume", err)
	}
}

func TestBtrfsEnsureCurrentCreatesSubvolume(t *testing.T) {
	root := t.TempDir()       // current does NOT exist yet
	fr := &execx.FakeRunner{} // default handler returns success
	be := NewBtrfs(fr)
	cur, err := be.EnsureCurrent(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if cur != filepath.Join(root, "current") {
		t.Fatalf("cur = %q", cur)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Name != "btrfs" ||
		strings.Join(fr.Calls[0].Args, " ") != "subvolume create "+filepath.Join(root, "current") {
		t.Fatalf("expected `btrfs subvolume create <cur>`, got %+v", fr.Calls)
	}
}

func TestBtrfsDeleteInvokesSubvolumeDelete(t *testing.T) {
	fr := &execx.FakeRunner{}
	be := NewBtrfs(fr)
	target := "/data/x/snapshots/2026-06-24_030000"
	if err := be.Delete(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Name != "btrfs" ||
		strings.Join(fr.Calls[0].Args, " ") != "subvolume delete "+target {
		t.Fatalf("expected `btrfs subvolume delete <target>`, got %+v", fr.Calls)
	}
}

func TestDetectChoosesBackend(t *testing.T) {
	ctx := context.Background()
	okBtrfs := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		return execx.Result{}, nil // `btrfs --version` succeeds
	}}
	btrfsFS := func(string) (int64, error) { return BtrfsMagic, nil }
	if be := Detect(ctx, "/x", okBtrfs, btrfsFS); be.Name() != "btrfs" {
		t.Fatalf("want btrfs, got %s", be.Name())
	}
	ext4FS := func(string) (int64, error) { return 0xEF53, nil }
	if be := Detect(ctx, "/x", okBtrfs, ext4FS); be.Name() != "hardlink" {
		t.Fatalf("want hardlink on ext4, got %s", be.Name())
	}
	// btrfs FS but no btrfs binary -> hardlink
	noBin := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		return execx.Result{Code: 127}, errors.New("not found")
	}}
	if be := Detect(ctx, "/x", noBin, btrfsFS); be.Name() != "hardlink" {
		t.Fatalf("want hardlink when btrfs missing, got %s", be.Name())
	}
}
