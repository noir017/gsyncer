package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gsync/internal/execx"
)

// mkdirCp is a FakeRunner handler that emulates `cp -al` by creating the
// destination directory (always the last arg), so the atomic temp+rename path
// in Create has a real directory to rename.
func mkdirCp(name string, args []string) (execx.Result, error) {
	if name == "cp" {
		_ = os.MkdirAll(args[len(args)-1], 0o755)
	}
	return execx.Result{}, nil
}

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
	fr := &execx.FakeRunner{Handler: mkdirCp}
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
	// cp copies into the .partial staging dir; Create then renames it to want.
	args := fr.Calls[0].Args
	if args[0] != "-al" || args[1] != filepath.Join(root, "current") || args[2] != want+".partial" {
		t.Fatalf("cp args = %v (want copy into %q)", args, want+".partial")
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("final snapshot dir not created: %v", err)
	}
	if _, err := os.Stat(want + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial dir should be gone after rename, stat err = %v", err)
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
	be := NewHardlink(&execx.FakeRunner{Handler: mkdirCp})
	snap, err := be.Create(context.Background(), root, ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030001")
	if snap != want {
		t.Fatalf("snap = %q, want %q (should bump to avoid collision)", snap, want)
	}
}

// A failed cp must leave neither the .partial staging dir nor a final snapshot
// dir behind — the previous good snapshot set stays clean.
func TestHardlinkCreateCleansUpPartialOnError(t *testing.T) {
	root := t.TempDir()
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "cp" {
			_ = os.MkdirAll(args[len(args)-1], 0o755) // partial gets created…
			return execx.Result{Code: 1}, errors.New("cp died midway")
		}
		return execx.Result{}, nil
	}}
	be := NewHardlink(fr)
	ts := time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC)
	if _, err := be.Create(context.Background(), root, ts); err == nil {
		t.Fatal("expected error when cp fails")
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("final dir must not exist after failed cp, stat err = %v", err)
	}
	if _, err := os.Stat(want + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial dir must be cleaned up after failed cp, stat err = %v", err)
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
