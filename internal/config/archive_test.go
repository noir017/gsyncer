// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// archiveCfg builds a one-entry config with the given data-model settings.
// local_path comes from t.TempDir() so it is absolute on every platform and the
// entry reaches the checks under test rather than tripping the local_path guard.
func archiveCfg(t *testing.T, mode string, removeSource *bool, d Defaults) *Config {
	t.Helper()
	return &Config{
		Defaults: d,
		Sync: []Sync{{
			Name: "cam", Host: "h", User: "u",
			// remote_path is always POSIX: it names a directory on the remote host,
			// never a path on the machine running the test.
			RemotePath: "/mnt/sd/record",
			LocalPath:  filepath.Join(t.TempDir(), "cam"),
			LocalMode:  mode, RemoveSourceFiles: removeSource,
		}},
	}
}

func boolPtr(b bool) *bool { return &b }

// The three meaningful combinations must all load.
func TestValidateAcceptsMeaningfulModeCombos(t *testing.T) {
	cases := []struct {
		name         string
		mode         string
		removeSource *bool
	}{
		{"mirror + keep remote (the default)", "", nil},
		{"mirror explicit + keep remote", LocalModeMirror, boolPtr(false)},
		{"archive + keep remote", LocalModeArchive, boolPtr(false)},
		{"archive + delete remote", LocalModeArchive, boolPtr(true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := archiveCfg(t, tc.mode, tc.removeSource, Defaults{}).Validate(); err != nil {
				t.Fatalf("should be valid: %v", err)
			}
		})
	}
}

// The fourth combination is a silent backup-destroyer and must be rejected at
// load time, not merely documented: run 1 empties the remote, run 2's --delete
// mirrors that emptiness over current/, and retention then ages out the
// snapshots that held the only remaining copies.
func TestValidateRejectsMirrorWithRemoteDeletion(t *testing.T) {
	err := archiveCfg(t, LocalModeMirror, boolPtr(true), Defaults{}).Validate()
	if err == nil {
		t.Fatal("mirror + remove_source_files must be a config error")
	}
	if !strings.Contains(err.Error(), "remove_source_files") ||
		!strings.Contains(err.Error(), LocalModeArchive) {
		t.Fatalf("error should name the field and point at the fix, got: %v", err)
	}
}

// The default local_mode is mirror, so leaving it unset and only enabling
// remote deletion hits the same trap and must be caught too.
func TestValidateRejectsImplicitMirrorWithRemoteDeletion(t *testing.T) {
	err := archiveCfg(t, "", boolPtr(true), Defaults{}).Validate()
	if err == nil {
		t.Fatal("implicit mirror + remove_source_files must be rejected")
	}
	if !strings.Contains(err.Error(), "remove_source_files") {
		t.Fatalf("wrong rejection reason: %v", err)
	}
}

// The guardrail is evaluated on effective values, so an entry that inherits the
// dangerous pair from [defaults] is caught as well.
func TestValidateRejectsDangerousComboInheritedFromDefaults(t *testing.T) {
	d := Defaults{LocalMode: LocalModeMirror, RemoveSourceFiles: true}
	err := archiveCfg(t, "", nil, d).Validate()
	if err == nil {
		t.Fatal("inherited mirror + remove_source_files must be rejected")
	}
	if !strings.Contains(err.Error(), "remove_source_files") {
		t.Fatalf("wrong rejection reason: %v", err)
	}
	// ...while defaults carrying remove_source_files is fine when the entry that
	// inherits it is an archive.
	d2 := Defaults{RemoveSourceFiles: true}
	if err := archiveCfg(t, LocalModeArchive, nil, d2).Validate(); err != nil {
		t.Fatalf("archive inheriting remove_source_files should be valid: %v", err)
	}
}

// An entry may opt back into mirror over an archiving default — but then it must
// also turn remote deletion off.
func TestValidateEntryOverridesDefaultArchive(t *testing.T) {
	d := Defaults{LocalMode: LocalModeArchive, RemoveSourceFiles: true}
	if err := archiveCfg(t, LocalModeMirror, nil, d).Validate(); err == nil {
		t.Fatal("entry reverting to mirror while inheriting remote deletion must be rejected")
	}
	if err := archiveCfg(t, LocalModeMirror, boolPtr(false), d).Validate(); err != nil {
		t.Fatalf("mirror entry that also disables remote deletion should be valid: %v", err)
	}
}

func TestValidateRejectsUnknownLocalMode(t *testing.T) {
	err := archiveCfg(t, "archve", nil, Defaults{}).Validate() // typo
	if err == nil {
		t.Fatal("unknown local_mode must be rejected")
	}
	if !strings.Contains(err.Error(), "local_mode") {
		t.Fatalf("error should name local_mode, got: %v", err)
	}
	// A typo in defaults is caught even when the entry overrides it, so the
	// mistake surfaces where it was written.
	d := Defaults{LocalMode: "mirrror"}
	if err := archiveCfg(t, LocalModeArchive, nil, d).Validate(); err == nil {
		t.Fatal("unknown defaults.local_mode must be rejected")
	}
}

// Deleting remote files is driven entirely by remote_path, so a relative or
// root path must not reach a run.
func TestValidateRemoveSourceRequiresSafeRemotePath(t *testing.T) {
	mk := func(remote string) *Config {
		c := archiveCfg(t, LocalModeArchive, boolPtr(true), Defaults{})
		c.Sync[0].RemotePath = remote
		return c
	}
	for _, bad := range []string{"record", "./record", "~/record", "/", "//"} {
		if err := mk(bad).Validate(); err == nil {
			t.Errorf("remote_path %q must be rejected with remove_source_files", bad)
		}
	}
	if err := mk("/mnt/sd/record").Validate(); err != nil {
		t.Fatalf("absolute remote_path should be valid: %v", err)
	}
	// The same paths are fine when nothing is being deleted remotely.
	c := archiveCfg(t, LocalModeArchive, boolPtr(false), Defaults{})
	c.Sync[0].RemotePath = "record"
	if err := c.Validate(); err != nil {
		t.Fatalf("relative remote_path is only restricted when deleting: %v", err)
	}
}

func TestEffectiveLocalModeAndRemoveSource(t *testing.T) {
	// entry over defaults, in both directions
	if got := (Sync{LocalMode: LocalModeArchive}).EffectiveLocalMode(Defaults{LocalMode: LocalModeMirror}); got != LocalModeArchive {
		t.Fatalf("entry override = %q", got)
	}
	if got := (Sync{LocalMode: LocalModeMirror}).EffectiveLocalMode(Defaults{LocalMode: LocalModeArchive}); got != LocalModeMirror {
		t.Fatalf("entry override = %q", got)
	}
	// unset falls back to defaults, then to mirror
	if got := (Sync{}).EffectiveLocalMode(Defaults{LocalMode: LocalModeArchive}); got != LocalModeArchive {
		t.Fatalf("defaults fallback = %q", got)
	}
	if got := (Sync{}).EffectiveLocalMode(Defaults{}); got != LocalModeMirror {
		t.Fatalf("zero value must be mirror, got %q", got)
	}
	if (Sync{}).IsArchive(Defaults{}) {
		t.Fatal("zero value must not be an archive")
	}

	if !(Sync{RemoveSourceFiles: boolPtr(true)}).EffectiveRemoveSourceFiles(Defaults{}) {
		t.Fatal("entry true should win")
	}
	if (Sync{RemoveSourceFiles: boolPtr(false)}).EffectiveRemoveSourceFiles(Defaults{RemoveSourceFiles: true}) {
		t.Fatal("entry false should win over default true")
	}
	if !(Sync{}).EffectiveRemoveSourceFiles(Defaults{RemoveSourceFiles: true}) {
		t.Fatal("nil should inherit default true")
	}
	if (Sync{}).EffectiveRemoveSourceFiles(Defaults{}) {
		t.Fatal("remote deletion must be off by default")
	}
}

// The starter config must itself be valid, including its archive example.
func TestStarterTemplateMentionsArchiveMode(t *testing.T) {
	for _, want := range []string{"local_mode", "archive", "remove_source_files"} {
		if !strings.Contains(StarterTemplate, want) {
			t.Errorf("starter template should document %q", want)
		}
	}
}
