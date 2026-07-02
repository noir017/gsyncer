package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/snapshot"
	"gsyncer/internal/syncer"
)

func manyEntryCfg(n int) *config.Config {
	c := &config.Config{}
	for i := 0; i < n; i++ {
		c.Sync = append(c.Sync, config.Sync{
			Name: fmt.Sprintf("e%02d", i), Host: "h", User: "u",
			RemotePath: "/r", LocalPath: fmt.Sprintf("/l%02d", i),
		})
	}
	return c
}

func TestListWindowsAroundCursor(t *testing.T) {
	m := newList(manyEntryCfg(20), &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 10}) // visibleRows = 5
	v := m.View()
	if !strings.Contains(v, "e00") || strings.Contains(v, "e05") {
		t.Fatalf("cursor at top should window to e00..e04:\n%s", v)
	}
	if !strings.Contains(v, "/ 20") {
		t.Fatalf("expected a scroll hint counting 20 rows:\n%s", v)
	}
	// jump to the bottom: the selected row must stay visible.
	for i := 0; i < 19; i++ {
		m, _ = m.Update(keyMsg("j"))
	}
	v = m.View()
	if !strings.Contains(v, "e19") {
		t.Fatalf("bottom cursor row must be visible:\n%s", v)
	}
	if strings.Contains(v, "e00") {
		t.Fatalf("top rows should be scrolled off:\n%s", v)
	}
}

func TestListTruncatesLongRows(t *testing.T) {
	long := "/very/long/local/path/that/certainly/exceeds/the/narrow/terminal/width"
	cfg := &config.Config{Sync: []config.Sync{
		{Name: "x", Host: "h", User: "u", RemotePath: "/r", LocalPath: long},
	}}
	m := newList(cfg, &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 100})
	v := m.View()
	if strings.Contains(v, long) {
		t.Fatalf("long path should be truncated, got:\n%s", v)
	}
	for _, ln := range strings.Split(v, "\n") {
		if strings.Contains(ln, "snaps") && lipgloss.Width(ln) > 30 {
			t.Fatalf("row exceeds width 30 (%d): %q", lipgloss.Width(ln), ln)
		}
	}
}

func TestShrinkMiddle(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"abcdef", 0, "abcdef"},  // w<=0: no limit
		{"abcdef", 10, "abcdef"}, // already fits
		{"abcdef", 1, "…"},
		{"abcdefghij", 5, "ab…ij"},
		{"abcdefghij", 6, "abc…ij"}, // head gets the extra column
	}
	for _, tc := range cases {
		if got := shrinkMiddle(tc.in, tc.w); got != tc.want {
			t.Errorf("shrinkMiddle(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
		if tc.w > 0 && lipgloss.Width(shrinkMiddle(tc.in, tc.w)) > tc.w {
			t.Errorf("shrinkMiddle(%q, %d) exceeds width", tc.in, tc.w)
		}
	}
}

func TestFitTwoDonatesSurplus(t *testing.T) {
	// a fits well under half the budget: b should get all the leftover space.
	a, b := fitTwo("ab", "0123456789", 10)
	if a != "ab" {
		t.Fatalf("short side must be untouched, got %q", a)
	}
	if w := lipgloss.Width(b); w > 8 {
		t.Fatalf("long side must shrink to the remainder (8), got %q (%d)", b, w)
	}
}

func TestListKeepsMetaAndPathEndsWhenNarrow(t *testing.T) {
	cfg := &config.Config{Sync: []config.Sync{{
		Name: "openwrt-backups", Host: "192.168.0.10", User: "root",
		RemotePath: "/mnt/sata2-4/backups",
		LocalPath:  "/mnt/disk2/volume16t/sync/backup/linux/openwrt/backups",
	}}}
	m := newList(cfg, &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	v := m.View()
	var row string
	for _, ln := range strings.Split(v, "\n") {
		if strings.Contains(ln, "openwrt-backups") {
			row = ln
			break
		}
	}
	if row == "" {
		t.Fatalf("entry row not rendered:\n%s", v)
	}
	if w := lipgloss.Width(row); w > 80 {
		t.Fatalf("row exceeds width 80 (%d): %q", w, row)
	}
	// The meta and both path tails must survive the shrink.
	for _, want := range []string{"snaps", "hardlink", "root@", "backups → ", "→ /mnt"} {
		if !strings.Contains(row, want) {
			t.Fatalf("narrow row must keep %q, got: %q", want, row)
		}
	}
}

func TestListNoClampWhenSizeUnknown(t *testing.T) {
	// Before any WindowSizeMsg, width/height are 0 → render every row, no cut.
	m := newList(manyEntryCfg(20), &execx.FakeRunner{}, nonBtrfsFS)
	v := m.View()
	if !strings.Contains(v, "e00") || !strings.Contains(v, "e19") {
		t.Fatalf("all rows must render when size is unknown:\n%s", v)
	}
}

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

func TestListDeleteKeyEmitsDeleteMsg(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	m, _ = m.Update(keyMsg("j")) // cursor -> 1
	_, cmd := m.Update(keyMsg("d"))
	if cmd == nil {
		t.Fatal("expected delete cmd")
	}
	msg := cmd()
	dm, ok := msg.(deleteEntryMsg)
	if !ok || dm.idx != 1 {
		t.Fatalf("got %#v, want deleteEntryMsg{idx:1}", msg)
	}
}

func TestListQOrEscRequestsQuit(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"q", keyMsg("q")},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
			_, cmd := m.Update(tc.key)
			if cmd == nil {
				t.Fatal("expected cmd")
			}
			if _, ok := cmd().(requestQuitMsg); !ok {
				t.Fatalf("key %q: expected requestQuitMsg, got %T", tc.name, cmd())
			}
		})
	}
}

func TestListCtrlCEmitsQuit(t *testing.T) {
	m := newList(twoEntryCfg(), &execx.FakeRunner{}, nonBtrfsFS)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected quit cmd")
	}
	if _, ok := cmd().(quitMsg); !ok {
		t.Fatalf("ctrl+c: expected quitMsg, got %T", cmd())
	}
}

func TestListFlagsInaccessibleIdentity(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-key")
	cfg := &config.Config{Sync: []config.Sync{
		{Name: "a", Host: "h", User: "u", Identity: missing, RemotePath: "/r", LocalPath: t.TempDir()},
		{Name: "b", Host: "h", User: "u", RemotePath: "/r", LocalPath: t.TempDir()},
	}}
	m := newList(cfg, &execx.FakeRunner{}, nonBtrfsFS)
	if _, bad := m.warn["a"]; !bad {
		t.Fatalf("entry a with missing identity should be flagged, warn=%v", m.warn)
	}
	if _, bad := m.warn["b"]; bad {
		t.Fatalf("entry b has no identity and must not be flagged, warn=%v", m.warn)
	}
	if v := m.View(); !strings.Contains(v, "not accessible") || !strings.Contains(v, "⚠") {
		t.Fatalf("View must surface the identity warning, got:\n%s", v)
	}
}

var _ = snapshot.RealFSType // keep import if unused elsewhere
