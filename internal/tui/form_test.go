package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gsyncer/internal/config"
)

func TestFormAppliesSize(t *testing.T) {
	m := newForm(baseCfg(), "x", 0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.width != 100 || m.height != 40 {
		t.Fatalf("size not stored: %dx%d", m.width, m.height)
	}
	if want := clampMin(100-14, 10); m.inputs[fName].Width != want {
		t.Fatalf("input width = %d, want %d", m.inputs[fName].Width, want)
	}
	if m.paste.Width != clampMin(100-14, 10) {
		t.Fatalf("paste width = %d", m.paste.Width)
	}
}

func TestFormSizeClampsTinyTerminal(t *testing.T) {
	// A tiny terminal must not produce a zero/negative width (textarea panics).
	m := newForm(baseCfg(), "x", 0)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 4, Height: 3})
	if m.inputs[fName].Width < 10 {
		t.Fatalf("input width must be clamped to a minimum, got %d", m.inputs[fName].Width)
	}
}

func baseCfg() *config.Config {
	return &config.Config{
		Sync: []config.Sync{{
			Name: "a", Host: "h", User: "u", RemotePath: "/r", LocalPath: "/l",
		}},
	}
}

func TestFormToSyncRetentionPointers(t *testing.T) {
	m := newForm(baseCfg(), "x", 0)
	m.ret[0].SetValue("5") // recent
	m.ret[2].SetValue("")  // semiannual stays empty
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.Retention == nil || s.Retention.Recent == nil || *s.Retention.Recent != 5 {
		t.Fatalf("recent override not set: %+v", s.Retention)
	}
	if s.Retention.Semiannual != nil {
		t.Fatalf("semiannual should be nil, got %v", *s.Retention.Semiannual)
	}
}

func TestFormShowsDefaultRetentionAsPlaceholder(t *testing.T) {
	cfg := baseCfg()
	cfg.Defaults.Retention = config.Retention{Recent: 7, Monthly: 6, Semiannual: 2, Yearly: 2}
	m := newForm(cfg, "x", 0) // entry 0 has no retention override
	for i, want := range []string{"7", "6", "2", "2"} {
		if got := m.ret[i].Placeholder; got != want {
			t.Fatalf("ret[%d] placeholder = %q, want %q", i, got, want)
		}
		if v := m.ret[i].Value(); v != "" {
			t.Fatalf("ret[%d] must stay empty (inherit default), got %q", i, v)
		}
	}
	if v := m.View(); !strings.Contains(v, "留空=默认") {
		t.Fatalf("View must hint that empty means default:\n%s", v)
	}
}

func TestFormToSyncNoOverride(t *testing.T) {
	m := newForm(baseCfg(), "x", 0)
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.Retention != nil {
		t.Fatalf("expected nil override, got %+v", s.Retention)
	}
}

func TestFormToSyncBadPort(t *testing.T) {
	m := newForm(baseCfg(), "x", 0)
	m.inputs[fPort].SetValue("abc")
	if _, err := m.toSync(); err == nil {
		t.Fatal("expected port parse error")
	}
}

func TestFormSaveRenameReplacesByIndex(t *testing.T) {
	cfg := baseCfg()
	path := filepath.Join(t.TempDir(), "config.toml")
	m := newForm(cfg, path, 0)
	m.inputs[fName].SetValue("renamed")
	if cmd := m.save(); cmd == nil {
		t.Fatalf("save failed: status=%q", m.status)
	}
	if len(cfg.Sync) != 1 {
		t.Fatalf("rename must not append; got %d entries", len(cfg.Sync))
	}
	if cfg.Sync[0].Name != "renamed" {
		t.Fatalf("name not updated: %q", cfg.Sync[0].Name)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Sync[0].Name != "renamed" {
		t.Fatalf("not persisted: %q", reloaded.Sync[0].Name)
	}
}

func TestFormSaveNewAppends(t *testing.T) {
	cfg := baseCfg()
	path := filepath.Join(t.TempDir(), "config.toml")
	m := newForm(cfg, path, -1)
	m.inputs[fName].SetValue("b")
	m.inputs[fHost].SetValue("h2")
	m.inputs[fUser].SetValue("u2")
	m.inputs[fRemote].SetValue("/r2")
	m.inputs[fLocal].SetValue("/l2")
	if cmd := m.save(); cmd == nil {
		t.Fatalf("save failed: status=%q", m.status)
	}
	if len(cfg.Sync) != 2 {
		t.Fatalf("expected append to 2, got %d", len(cfg.Sync))
	}
}

func TestFormSaveValidationFailsNoWrite(t *testing.T) {
	cfg := baseCfg()
	path := filepath.Join(t.TempDir(), "config.toml")
	m := newForm(cfg, path, 0)
	m.inputs[fLocal].SetValue("") // local_path required
	if cmd := m.save(); cmd != nil {
		t.Fatal("expected save to fail validation")
	}
	if m.status == "" {
		t.Fatal("expected status to be set on validation failure")
	}
	if cfg.Sync[0].LocalPath != "/l" {
		t.Fatal("in-memory cfg must be rolled back on failure")
	}
}

func TestFormNewSeedsDefaultPort(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{SSHPort: 2222}}
	m := newForm(cfg, "x", -1)
	if got := m.inputs[fPort].Value(); got != "2222" {
		t.Fatalf("new entry port = %q, want 2222", got)
	}
	// falls back to 22 when no default is configured
	m2 := newForm(&config.Config{}, "x", -1)
	if got := m2.inputs[fPort].Value(); got != "22" {
		t.Fatalf("new entry port = %q, want 22", got)
	}
}

func TestFormNewSeedsRetentionAndIgnore(t *testing.T) {
	// no configured defaults -> GFS fallback
	m := newForm(&config.Config{}, "x", -1)
	for i, want := range []string{"7", "6", "2", "2"} {
		if got := m.ret[i].Value(); got != want {
			t.Fatalf("ret[%d] = %q, want %q", i, got, want)
		}
	}
	if !strings.Contains(m.ignore.Value(), "__pycache__/") ||
		!strings.Contains(m.ignore.Value(), "node_modules/") {
		t.Fatalf("ignore defaults missing: %q", m.ignore.Value())
	}
	// the seeded override must survive round-trip into a config.Sync
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.Retention == nil || s.Retention.Recent == nil || *s.Retention.Recent != 7 {
		t.Fatalf("seeded retention not applied: %+v", s.Retention)
	}
}

func TestFormNewSeedsRetentionFromConfigDefaults(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{
		Retention: config.Retention{Recent: 3, Monthly: 4, Semiannual: 5, Yearly: 6},
	}}
	m := newForm(cfg, "x", -1)
	for i, want := range []string{"3", "4", "5", "6"} {
		if got := m.ret[i].Value(); got != want {
			t.Fatalf("ret[%d] = %q, want %q (should mirror config defaults)", i, got, want)
		}
	}
}

func TestFormEditDoesNotSeedDefaults(t *testing.T) {
	// editing an existing entry must not inject defaults over its real values
	m := newForm(baseCfg(), "x", 0)
	if m.ignore.Value() != "" {
		t.Fatalf("edit form must not seed ignore defaults, got %q", m.ignore.Value())
	}
	if m.ret[0].Value() != "" {
		t.Fatalf("edit form must not seed retention defaults, got %q", m.ret[0].Value())
	}
}

func TestFormIgnoreArrowsMoveCursorThenLeaveAtBoundary(t *testing.T) {
	m := newForm(&config.Config{}, "x", -1) // 6 default ignore lines
	m.focus = focusIgnore
	m.applyFocus()

	// park the cursor on the top line
	for i := 0; i < 20; i++ {
		m.ignore.CursorUp()
	}
	if m.ignore.Line() != 0 {
		t.Fatalf("setup: cursor not on top line, got %d", m.ignore.Line())
	}

	// ↓ from a non-last line moves the cursor, focus stays in the box
	m, _ = m.Update(keyMsg("down"))
	if m.focus != focusIgnore {
		t.Fatalf("down (mid box) changed focus to %d", m.focus)
	}
	if m.ignore.Line() != 1 {
		t.Fatalf("down should move cursor to line 1, got %d", m.ignore.Line())
	}

	// ↑ back to the top line stays in the box
	m, _ = m.Update(keyMsg("up"))
	if m.focus != focusIgnore || m.ignore.Line() != 0 {
		t.Fatalf("up should return cursor to top, focus=%d line=%d", m.focus, m.ignore.Line())
	}

	// ↑ on the top line leaves the box for the previous field
	m, _ = m.Update(keyMsg("up"))
	if m.focus == focusIgnore {
		t.Fatal("up on first line should leave the ignore box")
	}
}

func TestFormIgnoreDownOnLastLineLeavesBox(t *testing.T) {
	m := newForm(&config.Config{}, "x", -1)
	m.focus = focusIgnore
	m.applyFocus()
	for i := 0; i < 20; i++ {
		m.ignore.CursorDown()
	}
	if m.ignore.Line() != m.ignore.LineCount()-1 {
		t.Fatalf("setup: cursor not on last line, got %d", m.ignore.Line())
	}
	m, _ = m.Update(keyMsg("down"))
	if m.focus == focusIgnore {
		t.Fatal("down on last line should leave the ignore box")
	}
}

func TestFormArrowsNavigateOtherFields(t *testing.T) {
	m := newForm(baseCfg(), "x", 0)
	m.focus = fName
	m.applyFocus()
	m, _ = m.Update(keyMsg("down"))
	if m.focus != fHost {
		t.Fatalf("down on name field should move to host (%d), got %d", fHost, m.focus)
	}
}

func TestParsePasteKeyValue(t *testing.T) {
	got := parsePaste("name=foo host=1.2.3.4 port=2200 user=root identity=~/.ssh/id remote=/data local=~/d")
	want := map[int]string{
		fName: "foo", fHost: "1.2.3.4", fPort: "2200", fUser: "root",
		fIdentity: "~/.ssh/id", fRemote: "/data", fLocal: "~/d",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("field %d = %q, want %q", k, got[k], v)
		}
	}
}

func TestParsePasteScpShorthand(t *testing.T) {
	got := parsePaste("root@example.com:/srv/www")
	if got[fUser] != "root" || got[fHost] != "example.com" || got[fRemote] != "/srv/www" {
		t.Fatalf("scp parse = %+v", got)
	}
}

func TestApplyPasteFillsInputs(t *testing.T) {
	m := newForm(&config.Config{}, "x", -1)
	m.paste.SetValue("host=h user=u remote=/r")
	m.applyPaste()
	if m.inputs[fHost].Value() != "h" || m.inputs[fUser].Value() != "u" || m.inputs[fRemote].Value() != "/r" {
		t.Fatal("applyPaste did not fill inputs")
	}
	if m.paste.Value() != "" {
		t.Fatal("paste field should be cleared after parse")
	}
}

func TestNewFormCopyIsNewWithUniqueName(t *testing.T) {
	cfg := baseCfg() // one entry named "a"
	m := newFormCopy(cfg, "x", 0)
	if m.origIdx != -1 {
		t.Fatalf("copy origIdx = %d, want -1 (new entry)", m.origIdx)
	}
	if got := m.inputs[fName].Value(); got != "a-copy" {
		t.Fatalf("copy name = %q, want a-copy", got)
	}
	if m.inputs[fHost].Value() != "h" {
		t.Fatal("copy should carry over host from source")
	}
	if !m.isDirty() {
		t.Fatal("a populated copy should read as dirty")
	}
}

func TestFormIsDirty(t *testing.T) {
	m := newForm(baseCfg(), "x", 0)
	if m.isDirty() {
		t.Fatal("fresh form must not be dirty")
	}
	m.inputs[fName].SetValue("changed")
	if !m.isDirty() {
		t.Fatal("changed form must be dirty")
	}
}
