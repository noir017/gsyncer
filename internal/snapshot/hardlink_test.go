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

// mkdirCp is a FakeRunner handler that emulates a non-CoW filesystem: the
// reflink probe (`cp --reflink=always`) fails, and the real `cp -al` copy is
// emulated by creating the destination directory (always the last arg) so the
// atomic temp+rename path in Create has a real directory to rename.
func mkdirCp(name string, args []string) (execx.Result, error) {
	if name == "cp" {
		if len(args) > 0 && args[0] == "--reflink=always" {
			return execx.Result{Code: 1}, errors.New("reflink not supported")
		}
		_ = os.MkdirAll(args[len(args)-1], 0o755)
	}
	return execx.Result{}, nil
}

// lastCp returns the final cp invocation recorded (the real copy, after the
// reflink probe).
func lastCp(t *testing.T, calls []execx.Call) execx.Call {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Name == "cp" {
			return calls[i]
		}
	}
	t.Fatalf("no cp call recorded in %+v", calls)
	return execx.Call{}
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
	// The real copy is the last cp call (a reflink probe precedes it and fails on
	// this emulated non-CoW fs). It copies into the .partial staging dir; Create
	// then renames it to want.
	args := lastCp(t, fr.Calls).Args
	if args[0] != "-al" || args[1] != filepath.Join(root, "current") || args[2] != want+".partial" {
		t.Fatalf("cp args = %v (want copy into %q)", args, want+".partial")
	}
	if be.(*Hardlink).Mode() != "hardlink" {
		t.Fatalf("mode = %q, want hardlink on non-CoW fs", be.(*Hardlink).Mode())
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("final snapshot dir not created: %v", err)
	}
	if _, err := os.Stat(want + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial dir should be gone after rename, stat err = %v", err)
	}
}

// On a CoW filesystem the reflink probe succeeds, so Create must use an
// independent `cp --reflink=always -a` copy and report mode "reflink".
func TestHardlinkCreateUsesReflinkWhenSupported(t *testing.T) {
	root := t.TempDir()
	// Every cp succeeds (probe + real copy); emulate the copy by creating the
	// destination so the temp+rename path completes.
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "cp" {
			_ = os.MkdirAll(args[len(args)-1], 0o755)
		}
		return execx.Result{}, nil
	}}
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
	args := lastCp(t, fr.Calls).Args
	if len(args) != 4 || args[0] != "--reflink=always" || args[1] != "-a" ||
		args[2] != filepath.Join(root, "current") || args[3] != want+".partial" {
		t.Fatalf("copy args = %v (want reflink copy into %q)", args, want+".partial")
	}
	if be.(*Hardlink).Mode() != "reflink" {
		t.Fatalf("mode = %q, want reflink on CoW fs", be.(*Hardlink).Mode())
	}
	// The probe files must not linger in the snapshots dir.
	if _, err := os.Stat(filepath.Join(root, "snapshots", ".gsync-reflink-probe")); !os.IsNotExist(err) {
		t.Fatalf("probe file left behind, stat err = %v", err)
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
