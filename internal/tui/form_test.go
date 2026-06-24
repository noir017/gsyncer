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
