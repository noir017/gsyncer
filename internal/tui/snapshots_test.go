package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
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

func TestSnapsResizesTable(t *testing.T) {
	e := makeSnaps(t)
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.tbl.Width() != 116 {
		t.Fatalf("tbl.Width() = %d, want 116", m.tbl.Width())
	}
	// SetHeight(30) sets viewport.Height = 30 - 1 (header row), so Height() returns 29.
	if m.tbl.Height() != 29 {
		t.Fatalf("tbl.Height() = %d, want 29 (SetHeight(30) minus 1 header line)", m.tbl.Height())
	}
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

// Sizes are computed off the UI goroutine: the list opens with "…" placeholders
// and fills in only when the size command's message is delivered.
func TestSnapsSizesLoadAsync(t *testing.T) {
	e := makeSnaps(t, "2026-06-24_030000") // one 10-byte file
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)

	// Before the size command runs, the row shows the pending placeholder.
	if got := m.tbl.Rows()[0][1]; got != "…" {
		t.Fatalf("size cell before compute = %q, want the pending placeholder", got)
	}

	cmd := m.Init() // background size computation
	if cmd == nil {
		t.Fatal("Init should return a size-computation command")
	}
	m, _ = m.Update(cmd())

	if got := m.tbl.Rows()[0][1]; got == "…" || got == "" {
		t.Fatalf("size cell after compute = %q, want a real size", got)
	}
}

// A size result from a superseded computation (older epoch) must be ignored so
// it can't overwrite the rows after the list changed.
func TestSnapsStaleSizesIgnored(t *testing.T) {
	e := makeSnaps(t, "2026-06-24_030000")
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	m.epoch = 5
	m, _ = m.Update(snapSizesMsg{epoch: 4, sizes: map[string]string{"2026-06-24_030000": "999 B"}})
	if got := m.tbl.Rows()[0][1]; got == "999 B" {
		t.Fatal("stale (older-epoch) size result must be ignored")
	}
}

func TestSnapsDeleteRemovesDir(t *testing.T) {
	e := makeSnaps(t, "2026-06-24_030000")
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	p := m.snapPath(0)
	// 'd' then confirm 'y' — deletion now runs off the UI goroutine as a Cmd, so
	// execute the returned Cmd and feed its done-msg back before asserting.
	m, _ = m.Update(keyMsg("d"))
	m, cmd := m.Update(keyMsg("y"))
	if cmd == nil {
		t.Fatal("confirming delete should return a background delete command")
	}
	m, _ = m.Update(cmd()) // runs the delete, then reloads the list
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir should be deleted: %v", err)
	}
	if len(m.times) != 0 {
		t.Fatalf("times should be empty after delete, got %d", len(m.times))
	}
}

func TestSnapsPruneConfirmsBeforeDeleting(t *testing.T) {
	// three snapshots + an all-zero policy → safety floor keeps the newest,
	// so two are prunable.
	e := makeSnaps(t, "2026-06-22_030000", "2026-06-23_030000", "2026-06-24_030000")
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	p0, p1 := m.snapPath(0), m.snapPath(1)

	// 'p' must ARM a confirmation, not delete anything.
	m, _ = m.Update(keyMsg("p"))
	if !m.confirmPrune {
		t.Fatal("p must set confirmPrune")
	}
	if m.pendingPruneN != 2 {
		t.Fatalf("pendingPruneN = %d, want 2", m.pendingPruneN)
	}
	if _, err := os.Stat(p0); err != nil {
		t.Fatal("nothing must be deleted before confirmation")
	}
	if !strings.Contains(m.View(), "将删除 2 份") {
		t.Fatalf("view must show the count prompt, got:\n%s", m.View())
	}

	// 'y' confirms → prune runs off the UI goroutine; execute the returned Cmd
	// and feed its done-msg back before asserting.
	m, cmd := m.Update(keyMsg("y"))
	if m.confirmPrune {
		t.Fatal("confirmPrune must clear after y")
	}
	if cmd == nil {
		t.Fatal("confirming prune should return a background prune command")
	}
	m, _ = m.Update(cmd())
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Fatalf("older snapshot should be pruned: %v", err)
	}
	if len(m.times) != 1 {
		t.Fatalf("times after prune = %d, want 1", len(m.times))
	}
	if !strings.Contains(m.status, "已清理 2") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestSnapsPruneCancelKeepsAll(t *testing.T) {
	e := makeSnaps(t, "2026-06-23_030000", "2026-06-24_030000")
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(keyMsg("p"))
	if !m.confirmPrune {
		t.Fatal("precondition: confirmPrune set")
	}
	m, _ = m.Update(keyMsg("n"))
	if m.confirmPrune {
		t.Fatal("confirmPrune must clear after n")
	}
	if len(m.times) != 2 {
		t.Fatalf("no snapshot may be deleted on cancel, times = %d", len(m.times))
	}
	if !strings.Contains(m.status, "已取消") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestSnapsPruneNothingToDo(t *testing.T) {
	// a single snapshot is always kept by the safety floor → nothing prunable.
	e := makeSnaps(t, "2026-06-24_030000")
	m := newSnaps(e, config.Defaults{}, &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(keyMsg("p"))
	if m.confirmPrune {
		t.Fatal("no confirmation should arm when nothing is prunable")
	}
	if !strings.Contains(m.status, "无可清理") {
		t.Fatalf("status = %q", m.status)
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

	// press enter to accept the default path; the restore runs off the UI
	// goroutine, so execute the returned Cmd to perform the cp.
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("accepting the restore path should return a background restore command")
	}
	cmd()

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

func TestSnapsRestoreConfirmsExistingDest(t *testing.T) {
	const ts = "2026-06-24_030000"
	e := makeSnaps(t, ts)
	fr := &execx.FakeRunner{}
	m := newSnaps(e, config.Defaults{}, fr, nonBtrfsFS)

	// create an existing destination directory
	existingDst := filepath.Join(e.LocalPath, "existing")
	if err := os.MkdirAll(existingDst, 0o755); err != nil {
		t.Fatal(err)
	}

	// press x to enter restore mode
	m, _ = m.Update(keyMsg("x"))
	if !m.restoring {
		t.Fatal("should be in restoring mode after x")
	}

	// set destination to the existing directory
	m.restoreInput.SetValue(existingDst)

	// press enter — should transition to confirmOverwrite, not run cp
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.confirmOverwrite {
		t.Fatal("expected confirmOverwrite == true after enter with existing dst")
	}
	if m.restoring {
		t.Fatal("expected restoring == false after transitioning to confirmOverwrite")
	}
	for _, c := range fr.Calls {
		if c.Name == "cp" {
			t.Fatalf("cp must not be called before confirmation, got call: %+v", c)
		}
	}

	// press y to confirm — should issue cp (off the UI goroutine)
	m, cmd := m.Update(keyMsg("y"))

	if m.confirmOverwrite {
		t.Fatal("expected confirmOverwrite == false after y")
	}
	if cmd == nil {
		t.Fatal("confirming overwrite should return a background restore command")
	}
	cmd()
	var cpCall *execx.Call
	for i := range fr.Calls {
		if fr.Calls[i].Name == "cp" {
			cpCall = &fr.Calls[i]
			break
		}
	}
	if cpCall == nil {
		t.Fatal("expected a cp call after y, got none")
	}
	dst := cpCall.Args[len(cpCall.Args)-1]
	if dst != existingDst {
		t.Fatalf("cp dst = %q, want %q", dst, existingDst)
	}
}

func TestSnapsRestoreOverwriteCancel(t *testing.T) {
	const ts = "2026-06-24_030000"
	e := makeSnaps(t, ts)
	fr := &execx.FakeRunner{}
	m := newSnaps(e, config.Defaults{}, fr, nonBtrfsFS)

	// create an existing destination directory
	existingDst := filepath.Join(e.LocalPath, "existing")
	if err := os.MkdirAll(existingDst, 0o755); err != nil {
		t.Fatal(err)
	}

	// press x to enter restore mode
	m, _ = m.Update(keyMsg("x"))
	m.restoreInput.SetValue(existingDst)

	// press enter — transition to confirmOverwrite
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.confirmOverwrite {
		t.Fatal("expected confirmOverwrite == true")
	}

	// press n to cancel
	m, _ = m.Update(keyMsg("n"))

	if m.confirmOverwrite {
		t.Fatal("expected confirmOverwrite == false after n")
	}
	for _, c := range fr.Calls {
		if c.Name == "cp" {
			t.Fatalf("cp must not be called after cancel, got call: %+v", c)
		}
	}
	if !strings.Contains(m.status, "已取消") {
		t.Fatalf("status should mention 已取消, got %q", m.status)
	}
}

func TestSnapsRestoreNewDestNoConfirm(t *testing.T) {
	const ts = "2026-06-24_030000"
	e := makeSnaps(t, ts)
	fr := &execx.FakeRunner{}
	m := newSnaps(e, config.Defaults{}, fr, nonBtrfsFS)

	// destination that does NOT exist
	newDst := filepath.Join(e.LocalPath, "nonexistent-restore")

	// press x to enter restore mode
	m, _ = m.Update(keyMsg("x"))
	if !m.restoring {
		t.Fatal("should be in restoring mode after x")
	}
	m.restoreInput.SetValue(newDst)

	// press enter — should issue cp directly (off the UI goroutine) without confirm
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.confirmOverwrite {
		t.Fatal("confirmOverwrite should not be set for non-existing destination")
	}
	if cmd == nil {
		t.Fatal("restore to a new destination should return a background restore command")
	}
	cmd()
	var cpCall *execx.Call
	for i := range fr.Calls {
		if fr.Calls[i].Name == "cp" {
			cpCall = &fr.Calls[i]
			break
		}
	}
	if cpCall == nil {
		t.Fatal("expected a cp call for non-existing destination, got none")
	}
	dst := cpCall.Args[len(cpCall.Args)-1]
	if dst != newDst {
		t.Fatalf("cp dst = %q, want %q", dst, newDst)
	}
}
