package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
	"gsync/internal/execx"
)

// makeSnaps creates root/snapshots/<ts> dirs and returns the entry.
func makeSnaps(t *testing.T, names ...string) config.Sync {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		d := filepath.Join(root, "snapshots", n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f"), make([]byte, 10), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return config.Sync{Name: "x", Host: "h", User: "u", RemotePath: "/r", LocalPath: root}
}

func TestSnapsReloadDescending(t *testing.T) {
	e := makeSnaps(t, "2026-06-23_030000", "2026-06-24_030000")
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	if len(m.times) != 2 {
		t.Fatalf("want 2 times, got %d", len(m.times))
	}
	if m.times[0].Before(m.times[1]) {
		t.Fatal("times must be newest-first")
	}
}

func TestSnapsDeleteRemovesDir(t *testing.T) {
	e := makeSnaps(t, "2026-06-24_030000")
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	p := m.snapPath(0)
	// 'd' then confirm 'y'
	m, _ = m.Update(keyMsg("d"))
	m, _ = m.Update(keyMsg("y"))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir should be deleted: %v", err)
	}
	if len(m.times) != 0 {
		t.Fatalf("times should be empty after delete, got %d", len(m.times))
	}
}

func TestSnapsEscEmitsBack(t *testing.T) {
	e := makeSnaps(t)
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := cmd().(backToListMsg); !ok {
		t.Fatal("esc should emit backToListMsg")
	}
}
