package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gsync/internal/execx"
)

func TestHardlinkEnsureCurrentCreatesDir(t *testing.T) {
	root := t.TempDir()
	be := NewHardlink(&execx.FakeRunner{})
	cur, err := be.EnsureCurrent(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if cur != filepath.Join(root, "current") {
		t.Fatalf("cur = %q", cur)
	}
	if fi, err := os.Stat(cur); err != nil || !fi.IsDir() {
		t.Fatalf("current dir not created: %v", err)
	}
}

func TestHardlinkCreateInvokesCpAl(t *testing.T) {
	root := t.TempDir()
	fr := &execx.FakeRunner{}
	be := NewHardlink(fr)
	ts := time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC)
	snap, err := be.Create(context.Background(), root, ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if snap != want {
		t.Fatalf("snap = %q, want %q", snap, want)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Name != "cp" {
		t.Fatalf("expected cp call, got %+v", fr.Calls)
	}
	args := fr.Calls[0].Args
	if args[0] != "-al" || args[1] != filepath.Join(root, "current") || args[2] != want {
		t.Fatalf("cp args = %v", args)
	}
	if fi, err := os.Stat(filepath.Join(root, "snapshots")); err != nil || !fi.IsDir() {
		t.Fatalf("snapshots dir not created: %v", err)
	}
}

func TestHardlinkCreateAvoidsCollision(t *testing.T) {
	root := t.TempDir()
	ts := time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC)
	// Simulate a snapshot already taken in the same second.
	taken := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if err := os.MkdirAll(taken, 0o755); err != nil {
		t.Fatal(err)
	}
	be := NewHardlink(&execx.FakeRunner{})
	snap, err := be.Create(context.Background(), root, ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030001")
	if snap != want {
		t.Fatalf("snap = %q, want %q (should bump to avoid collision)", snap, want)
	}
}

func TestHardlinkNameAndDelete(t *testing.T) {
	be := NewHardlink(&execx.FakeRunner{})
	if be.Name() != "hardlink" {
		t.Fatalf("name = %q", be.Name())
	}
	root := t.TempDir()
	target := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := be.Delete(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("delete did not remove snapshot")
	}
}
