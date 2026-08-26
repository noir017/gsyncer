// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package syncer

import (
	"context"
	"strings"
	"testing"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/mnt/rec":      "'/mnt/rec'",
		"/mnt/my rec":   "'/mnt/my rec'",
		"/mnt/it's":     `'/mnt/it'\''s'`,
		"/mnt/$(evil)":  "'/mnt/$(evil)'",
		"/a; rm -rf /b": "'/a; rm -rf /b'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// The cleanup command must delete only already-empty directories, must never
// delete the configured root itself, and must collapse nested empties in one
// pass.
func TestRemotePruneCmd(t *testing.T) {
	got := remotePruneCmd("/mnt/sd/record")
	for _, want := range []string{
		"'/mnt/sd/record'", // quoted, not bare
		"-mindepth 1",      // never deletes remote_path itself
		"-depth",           // children before parents: nested empties in one pass
		"-type d",          // directories only
		"-empty",           // only dirs that already hold nothing
		"-delete",
		"-xdev", // don't wander across a mount point
	} {
		if !strings.Contains(got, want) {
			t.Errorf("remote prune cmd missing %q: %s", want, got)
		}
	}
	// A path with a quote in it stays a single argument to find.
	if q := remotePruneCmd("/mnt/it's"); !strings.Contains(q, `'/mnt/it'\''s'`) {
		t.Errorf("quote not escaped: %s", q)
	}
}

// The cleanup ssh call must reuse the same connection parameters as the
// transfer, or it could hit a different host/port or bypass the pinned
// known_hosts store.
func TestPruneRemoteEmptyDirsUsesSSHOptions(t *testing.T) {
	fr := &execx.FakeRunner{}
	deps := Deps{Runner: fr, Log: &captureLog{}, KnownHostsFile: "/cfg/known_hosts"}
	s := config.Sync{
		Name: "cam", User: "root", Host: "h", Identity: "/k",
		RemotePath: "/mnt/rec", StrictHostKey: true,
	}
	if err := pruneRemoteEmptyDirs(context.Background(), s, 2222, deps); err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Name != "ssh" {
		t.Fatalf("expected one ssh call, got %+v", fr.Calls)
	}
	j := strings.Join(fr.Calls[0].Args, " ")
	for _, want := range []string{
		"-p 2222", "BatchMode=yes", "ConnectTimeout=10",
		"StrictHostKeyChecking=yes", "UserKnownHostsFile=/cfg/known_hosts",
		"-i /k", "root@h", "-mindepth 1",
	} {
		if !strings.Contains(j, want) {
			t.Errorf("ssh args missing %q: %s", want, j)
		}
	}
}
