package tui

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSnapsRestoreDefaultPathNotCurrent(t *testing.T) {
	const ts = "2026-06-24_030000"
	e := makeSnaps(t, ts)
	fr := &execx.FakeRunner{}
	m := newSnaps(e, config.Defaults{}, fr, nonBtrfsFS)

	// press x to enter restore mode (default path is restore-<ts>)
	m, _ = m.Update(keyMsg("x"))
	if !m.restoring {
		t.Fatal("should be in restoring mode after x")
	}

	// press enter to accept the default path
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// find the cp call
	var cpCall *execx.Call
	for i := range fr.Calls {
		if fr.Calls[i].Name == "cp" {
			cpCall = &fr.Calls[i]
			break
		}
	}
	if cpCall == nil {
		t.Fatal("expected a cp call, got none")
	}

	dst := cpCall.Args[len(cpCall.Args)-1]
	wantDst := filepath.Join(e.LocalPath, "restore-"+ts)
	if dst != wantDst {
		t.Fatalf("cp dst = %q, want %q", dst, wantDst)
	}
	cur := filepath.Join(e.LocalPath, "current")
	if dst == cur {
		t.Fatalf("cp dst must not be current/ (%q)", cur)
	}
}

func TestSnapsRestoreBlocksCurrent(t *testing.T) {
	const ts = "2026-06-24_030000"
	e := makeSnaps(t, ts)
	fr := &execx.FakeRunner{}
	m := newSnaps(e, config.Defaults{}, fr, nonBtrfsFS)

	// press x to enter restore mode
	m, _ = m.Update(keyMsg("x"))
	if !m.restoring {
		t.Fatal("should be in restoring mode after x")
	}

	// set the restore input value to local/current
	m.restoreInput.SetValue(filepath.Join(e.LocalPath, "current"))

	// press enter — should be blocked
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// no cp call should have been issued
	for _, c := range fr.Calls {
		if c.Name == "cp" {
			t.Fatalf("cp must not be called when dst is current/, got call: %+v", c)
		}
	}

	// model must still be in restoring mode so the user can correct the path
	if !m.restoring {
		t.Fatal("model should remain in restoring mode after blocked restore")
	}

	// status must mention the problem
	if !strings.Contains(m.status, "current") && !strings.Contains(m.status, "不能覆盖") {
		t.Fatalf("status should mention current/不能覆盖, got %q", m.status)
	}
}
