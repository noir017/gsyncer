// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gsyncer/internal/config"
)

func scheduledFormCfg(t *testing.T) *config.Config {
	t.Helper()
	limit := 3
	return &config.Config{
		Defaults: config.Defaults{Schedule: "5 3 * * 2"},
		Sync: []config.Sync{{
			Name: "pacvue-record", Host: "tp-pacvue", User: "user",
			RemotePath: "/mnt/c/liqin/record",
			LocalPath:  filepath.Join(t.TempDir(), "record"),
			// Settings the form has no control for.
			Compress: boolPtr(true), Bwlimit: 512,
			PreSync: "echo pre", PostSync: "echo post",
			OfflineRetryLimit: &limit,
			// Settings it does control.
			Schedule: "0 16 * * 1", OnOffline: config.OfflineRetry, OfflineRetryInterval: "24h",
		}},
	}
}

// Before the form started from the original entry, any edit silently dropped
// compress, bwlimit and both hooks — and would have dropped the scheduling
// fields the same way.
func TestFormEditPreservesFieldsWithoutControls(t *testing.T) {
	cfg := scheduledFormCfg(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	m := newForm(cfg, path, 0)
	m.inputs[fHost].SetValue("tp-pacvue2")
	if cmd := m.save(); cmd == nil {
		t.Fatalf("save failed: %s", m.status)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := reloaded.Sync[0]
	if s.Host != "tp-pacvue2" {
		t.Fatalf("the edit itself was lost: %q", s.Host)
	}
	if s.Compress == nil || !*s.Compress || s.Bwlimit != 512 || s.PreSync != "echo pre" || s.PostSync != "echo post" {
		t.Fatalf("fields without form controls were dropped: %+v", s)
	}
	if s.OfflineRetryLimit == nil || *s.OfflineRetryLimit != 3 {
		t.Fatalf("offline_retry_limit dropped: %v", s.OfflineRetryLimit)
	}
	if s.Schedule != "0 16 * * 1" || s.OnOffline != config.OfflineRetry || s.OfflineRetryInterval != "24h" {
		t.Fatalf("scheduling fields lost: %+v", s)
	}
}

func TestFormLoadsAndEditsScheduling(t *testing.T) {
	cfg := scheduledFormCfg(t)
	m := newForm(cfg, filepath.Join(t.TempDir(), "config.toml"), 0)
	if m.inputs[fSchedule].Value() != "0 16 * * 1" || m.offline != config.OfflineRetry || m.retry.Value() != "24h" {
		t.Fatalf("form did not load scheduling: sched=%q offline=%q retry=%q",
			m.inputs[fSchedule].Value(), m.offline, m.retry.Value())
	}

	m.focus = focusOffline
	m.applyFocus()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if m.offline != config.OfflineSkip {
		t.Fatalf("space should cycle retry -> skip, got %q", m.offline)
	}
	if !m.isDirty() {
		t.Fatal("changing the offline policy must mark the form dirty")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if m.offline != config.OfflineForce {
		t.Fatalf("skip -> force, got %q", m.offline)
	}
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.OnOffline != "" {
		t.Fatalf("choosing the default (force) should write no override, got %q", s.OnOffline)
	}
}

func TestFormNewEntryInheritsScheduleByDefault(t *testing.T) {
	cfg := scheduledFormCfg(t)
	m := newForm(cfg, filepath.Join(t.TempDir(), "config.toml"), -1)
	if !strings.Contains(m.inputs[fSchedule].Placeholder, "5 3 * * 2") {
		t.Fatalf("placeholder should show the inherited default, got %q", m.inputs[fSchedule].Placeholder)
	}
	m.inputs[fName].SetValue("new")
	m.inputs[fHost].SetValue("h")
	m.inputs[fUser].SetValue("u")
	m.inputs[fRemote].SetValue("/r")
	m.inputs[fLocal].SetValue(filepath.Join(t.TempDir(), "new"))
	s, err := m.toSync()
	if err != nil {
		t.Fatal(err)
	}
	if s.Schedule != "" || s.OnOffline != "" || s.Compress != nil {
		t.Fatalf("a new entry should inherit everything: %+v", s)
	}
	if got := s.EffectiveSchedule(cfg.Defaults); got != "5 3 * * 2" {
		t.Fatalf("effective schedule = %q", got)
	}
}

func TestFormRejectsInvalidSchedule(t *testing.T) {
	cfg := scheduledFormCfg(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	m := newForm(cfg, path, 0)
	m.inputs[fSchedule].SetValue("every monday")
	if cmd := m.save(); cmd != nil {
		t.Fatal("an invalid cron expression must not save")
	}
	if !strings.Contains(m.status, "schedule") {
		t.Fatalf("status should explain the error, got %q", m.status)
	}
	if cfg.Sync[0].Schedule != "0 16 * * 1" {
		t.Fatal("in-memory config must be untouched after a failed save")
	}
}
