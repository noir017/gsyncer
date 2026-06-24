package tui

import (
	"path/filepath"
	"testing"

	"gsync/internal/config"
)

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
