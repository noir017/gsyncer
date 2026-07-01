package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/syncer"
)

func TestCmdInitWritesLoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if code := cmdInit([]string{"-config", path}); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
	}
	// The starter config must load and validate cleanly so `gsync list` works.
	if _, err := config.Load(path); err != nil {
		t.Fatalf("starter config does not load: %v", err)
	}
	// 0600: owner-only, since it may reference identity key paths.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", fi.Mode().Perm())
	}
}

func TestCmdInitRefusesExistingWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit([]string{"-config", path}); code == 0 {
		t.Fatal("init must refuse to overwrite without -force")
	}
	if b, _ := os.ReadFile(path); string(b) != "# mine\n" {
		t.Fatal("existing config must be left untouched without -force")
	}
	if code := cmdInit([]string{"-config", path, "-force"}); code != 0 {
		t.Fatal("init -force must overwrite")
	}
	if b, _ := os.ReadFile(path); string(b) == "# mine\n" {
		t.Fatal("init -force must replace the file")
	}
}

func TestResolveConfigPath(t *testing.T) {
	if got := resolveConfigPath("", "/opt/app"); got != "/opt/app/config.toml" {
		t.Fatalf("got %q", got)
	}
	if got := resolveConfigPath("/custom.toml", "/opt/app"); got != "/custom.toml" {
		t.Fatalf("got %q", got)
	}
}

func TestSelectEntries(t *testing.T) {
	all := []config.Sync{
		{Name: "a", Host: "h1"},
		{Name: "b", Host: "h2"},
		{Name: "c", Host: "h1"},
	}
	if got := selectEntries(all, "", ""); len(got) != 3 {
		t.Fatalf("all: %d", len(got))
	}
	if got := selectEntries(all, "b", ""); len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("by name: %v", got)
	}
	if got := selectEntries(all, "", "h1"); len(got) != 2 {
		t.Fatalf("by server: %v", got)
	}
}

func TestSummaryLine(t *testing.T) {
	res := []syncer.Result{
		{Name: "a", OK: true},
		{Name: "b", OK: false, Err: errors.New("x")},
	}
	got := summaryLine(res, 3*time.Second)
	if !strings.Contains(got, "成功 1") || !strings.Contains(got, "失败 1") ||
		!strings.Contains(got, "3.0s") {
		t.Fatalf("summary = %q", got)
	}
}
