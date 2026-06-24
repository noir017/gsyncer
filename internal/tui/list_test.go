package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/snapshot"
	"gsync/internal/syncer"
)

func twoEntryCfg() *config.Config {
	return &config.Config{Sync: []config.Sync{
		{Name: "a", Host: "h", User: "u", RemotePath: "/r", LocalPath: "/la"},
		{Name: "b", Host: "h", User: "u", RemotePath: "/r", LocalPath: "/lb"},
	}}
}

func nonBtrfsFS(string) (int64, error) { return 0, nil }

func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestListCursorMovement(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(keyMsg("j"))
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	m, _ = m.Update(keyMsg("j")) // clamp at last
	if m.cursor != 1 {
		t.Fatalf("cursor should clamp at 1, got %d", m.cursor)
	}
	m, _ = m.Update(keyMsg("k"))
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
}

func TestListEditKeyEmitsEditMsg(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(keyMsg("j")) // cursor -> 1
	_, cmd := m.Update(keyMsg("e"))
	if cmd == nil {
		t.Fatal("expected edit cmd")
	}
	msg := cmd()
	em, ok := msg.(editEntryMsg)
	if !ok || em.idx != 1 {
		t.Fatalf("got %#v, want editEntryMsg{idx:1}", msg)
	}
}

func TestListAddKeyEmitsNewForm(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	_, cmd := m.Update(keyMsg("a"))
	if em, ok := cmd().(editEntryMsg); !ok || em.idx != -1 {
		t.Fatalf("expected editEntryMsg{idx:-1}")
	}
}

func TestListSyncKeyEmitsRunOne(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	_, cmd := m.Update(keyMsg("s"))
	rm, ok := cmd().(runEntriesMsg)
	if !ok || len(rm.entries) != 1 || rm.entries[0].Name != "a" {
		t.Fatalf("got %#v, want one-entry runEntriesMsg for a", cmd())
	}
}

func TestListSyncAllKeyEmitsRunAll(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	_, cmd := m.Update(keyMsg("S"))
	rm := cmd().(runEntriesMsg)
	if len(rm.entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(rm.entries))
	}
}

func TestListEnterEmitsOpenSnaps(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if om, ok := cmd().(openSnapsMsg); !ok || om.idx != 0 {
		t.Fatalf("expected openSnapsMsg{idx:0}")
	}
}

func TestListSetRunStatus(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	m.setRunStatus([]syncer.Result{{Name: "a", OK: true}, {Name: "b", OK: false}})
	if m.lastRun["a"] != runOK || m.lastRun["b"] != runFail {
		t.Fatalf("run status not recorded: %+v", m.lastRun)
	}
}

func TestListRefreshHardlinkBackend(t *testing.T) {
	cfg := &config.Config{Sync: []config.Sync{
		{Name: "a", Host: "h", User: "u", RemotePath: "/r", LocalPath: t.TempDir()},
	}}
	m := newList(cfg, &execx.FakeRunner{}, nonBtrfsFS)
	m.refresh()
	if m.backends[cfg.Sync[0].LocalPath] != "hardlink" {
		t.Fatalf("want hardlink, got %q", m.backends[cfg.Sync[0].LocalPath])
	}
	if m.counts[cfg.Sync[0].LocalPath] != 0 {
		t.Fatalf("want 0 snaps, got %d", m.counts[cfg.Sync[0].LocalPath])
	}
}

var _ = snapshot.RealFSType // keep import if unused elsewhere
