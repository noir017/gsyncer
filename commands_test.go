package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Note: init is covered by cli_test.go (TestCmdInit*). These tests cover the
// check and restore commands added alongside it.

func TestCmdCheckValidAndInvalid(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.toml")
	if rc := cmdInit([]string{"--config", good}); rc != 0 {
		t.Fatalf("init rc = %d", rc)
	}
	if rc := cmdCheck([]string{"--config", good}); rc != 0 {
		t.Fatalf("check on valid config rc = %d, want 0", rc)
	}

	bad := filepath.Join(dir, "bad.toml")
	// missing required host/user/remote_path/local_path
	if err := os.WriteFile(bad, []byte("[[sync]]\n  name = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := cmdCheck([]string{"--config", bad}); rc != 1 {
		t.Fatalf("check on invalid config rc = %d, want 1", rc)
	}
}

func TestCmdRestoreRequiresFlags(t *testing.T) {
	// missing --to
	if rc := cmdRestore([]string{"--name", "web"}); rc != 2 {
		t.Fatalf("missing --to rc = %d, want 2", rc)
	}
	// both selectors
	if rc := cmdRestore([]string{"--name", "web", "--to", "/tmp/x", "--latest", "--at", "2026-06-24_030000"}); rc != 2 {
		t.Fatalf("both selectors rc = %d, want 2", rc)
	}
	// neither selector
	if rc := cmdRestore([]string{"--name", "web", "--to", "/tmp/x"}); rc != 2 {
		t.Fatalf("neither selector rc = %d, want 2", rc)
	}
}

func TestCmdRestoreHappyPath(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "web")
	snap := filepath.Join(local, "snapshots", "2026-06-24_030000")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.toml")
	cfg := "[[sync]]\n" +
		"  name = \"web\"\n  host = \"h\"\n  user = \"u\"\n" +
		"  remote_path = \"/r\"\n  local_path = \"" + local + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "restored")
	rc := cmdRestore([]string{"--config", cfgPath, "--name", "web", "--latest", "--to", dst})
	if rc != 0 {
		t.Fatalf("restore rc = %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dst, "hello.txt")); err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
}
