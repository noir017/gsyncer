package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListParsesSnapshotDirs(t *testing.T) {
	root := t.TempDir()
	snaps := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(filepath.Join(snaps, "2026-06-24_030000"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(snaps, "2026-05-01_010000"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a non-snapshot dir and a file must be ignored
	_ = os.MkdirAll(filepath.Join(snaps, "notatimestamp"), 0o755)
	_ = os.WriteFile(filepath.Join(snaps, "x.txt"), []byte("y"), 0o644)

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %v", len(got), got)
	}
}

func TestListMissingDirReturnsEmpty(t *testing.T) {
	got, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("missing snapshots dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}
