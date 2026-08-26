// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package tui

import (
	"path/filepath"
	"testing"

	"gsyncer/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// archiveFormCfg is a config whose single entry archives locally and drains the
// remote.
func archiveFormCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Sync: []config.Sync{{
			Name: "cam", Host: "h", User: "u",
			RemotePath: "/mnt/sd/record",
			LocalPath:  filepath.Join(t.TempDir(), "cam"),
			LocalMode:  config.LocalModeArchive,
			// Explicit false: the TUI must round-trip the *set* value, not lose it
			// to the "differs from default?" shortcut.
			RemoveSourceFiles: boolPtr(true),
		}},
	}
}

// The form rebuilds the whole entry from its fields, so an unrepresented
// setting would be silently dropped on save. For local_mode that is not a
// cosmetic loss: an archive downgraded to a mirror gets --delete on the next
// run, which wipes the accumulated archive.
func TestFormEditPreservesArchiveSettings(t *testing.T) {
	cfg := archiveFormCfg(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	m := newForm(cfg, path, 0)

	if !m.archive || !m.removeSrc {
		t.Fatalf("form did not load the entry's data model: archive=%v removeSrc=%v", m.archive, m.removeSrc)
	}
	// An edit to an unrelated field must not disturb the data model.
	m.inputs[fHost].SetValue("h2")
	if cmd := m.save(); cmd == nil {
		t.Fatalf("save failed: %s", m.status)
	}
	got := cfg.Sync[0]
	if !got.IsArchive(cfg.Defaults) {
		t.Fatalf("archive mode lost on save: local_mode=%q", got.LocalMode)
	}
	if !got.EffectiveRemoveSourceFiles(cfg.Defaults) {
		t.Fatal("remove_source_files lost on save")
	}
	// ...and it must survive the round-trip through the file, too.
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Sync[0].IsArchive(reloaded.Defaults) ||
		!reloaded.Sync[0].EffectiveRemoveSourceFiles(reloaded.Defaults) {
		t.Fatalf("not persisted: %+v", reloaded.Sync[0])
	}
}

// Toggling archive off must also clear remote deletion: the pair the user would
// otherwise be left holding is exactly the one Validate rejects, and failing at
// save time with a wall of text is a worse experience than fixing it live.
func TestFormTogglingArchiveOffClearsRemoteDeletion(t *testing.T) {
	cfg := archiveFormCfg(t)
	m := newForm(cfg, filepath.Join(t.TempDir(), "c.toml"), 0)
	m.focus = focusArchive
	m.applyFocus()

	m, _ = m.Update(keyMsg(" "))
	if m.archive {
		t.Fatal("space should have turned archive off")
	}
	if m.removeSrc {
		t.Fatal("remote deletion must be cleared when leaving archive mode")
	}
	if m.status == "" {
		t.Fatal("the user should be told why the second toggle flipped")
	}
	// The resulting entry is a plain mirror and saves cleanly.
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.IsArchive(cfg.Defaults) || s.EffectiveRemoveSourceFiles(cfg.Defaults) {
		t.Fatalf("expected a plain mirror, got %+v", s)
	}
}

// The converse: switching remote deletion on implies the archive model.
func TestFormEnablingRemoteDeletionSwitchesToArchive(t *testing.T) {
	cfg := &config.Config{Sync: []config.Sync{{
		Name: "a", Host: "h", User: "u", RemotePath: "/r",
		LocalPath: filepath.Join(t.TempDir(), "a"),
	}}}
	m := newForm(cfg, filepath.Join(t.TempDir(), "c.toml"), 0)
	if m.archive || m.removeSrc {
		t.Fatal("setup: entry should start as a plain mirror")
	}
	m.focus = focusRemoveSrc
	m.applyFocus()

	m, _ = m.Update(keyMsg(" "))
	if !m.removeSrc {
		t.Fatal("space should have turned remote deletion on")
	}
	if !m.archive {
		t.Fatal("enabling remote deletion must imply archive mode")
	}
	// Whatever the toggles produce must always be a config Validate accepts.
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	cand := *cfg
	cand.Sync = []config.Sync{s}
	if err := cand.Validate(); err != nil {
		t.Fatalf("the form must never build an invalid combination: %v", err)
	}
}

// A new entry follows [defaults], so a project that archives by default does not
// need every entry re-ticked — and cannot silently get a mirror.
func TestFormNewEntryInheritsDefaultDataModel(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{
		LocalMode: config.LocalModeArchive, RemoveSourceFiles: true,
	}}
	m := newForm(cfg, "x", -1)
	if !m.archive || !m.removeSrc {
		t.Fatalf("new entry should inherit defaults: archive=%v removeSrc=%v", m.archive, m.removeSrc)
	}
	// Inherited values need no entry-level override written out.
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.LocalMode != "" || s.RemoveSourceFiles != nil {
		t.Fatalf("inherited values should not be written as overrides: %+v", s)
	}
	if !s.IsArchive(cfg.Defaults) || !s.EffectiveRemoveSourceFiles(cfg.Defaults) {
		t.Fatalf("effective values wrong: %+v", s)
	}
}

// Opting out of an archiving default must be written explicitly, so a later edit
// to [defaults] cannot flip this entry back.
func TestFormMirrorOverArchiveDefaultIsExplicit(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{LocalMode: config.LocalModeArchive}}
	m := newForm(cfg, "x", -1)
	m.archive = false
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.LocalMode != config.LocalModeMirror {
		t.Fatalf("opting out of an archiving default must be explicit, got %q", s.LocalMode)
	}
}

// Both toggles participate in dirty detection, or esc would discard a change to
// them without warning.
func TestFormDataModelTogglesMarkDirty(t *testing.T) {
	cfg := &config.Config{Sync: []config.Sync{{
		Name: "a", Host: "h", User: "u", RemotePath: "/r",
		LocalPath: filepath.Join(t.TempDir(), "a"),
	}}}
	m := newForm(cfg, "x", 0)
	if m.isDirty() {
		t.Fatal("fresh form must not be dirty")
	}
	m.archive = true
	if !m.isDirty() {
		t.Fatal("toggling archive must mark the form dirty")
	}
	m.archive = false
	m.removeSrc = true
	if !m.isDirty() {
		t.Fatal("toggling remote deletion must mark the form dirty")
	}
}
