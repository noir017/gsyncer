package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b"), make([]byte, 50), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := dirSize(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != 150 {
		t.Fatalf("got %d, want 150", got)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		2355:    "2.3 KB",
		1048576: "1.0 MB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
