// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
)

// archiveDefaults is a config.Defaults for an entry that archives locally and
// drains the remote — the rolling-window-remote / permanent-local-archive case.
func archiveEntry(t *testing.T) (config.Sync, config.Defaults) {
	t.Helper()
	s := okEntry(t)
	s.RemotePath = "/mnt/sd/record"
	yes := true
	s.LocalMode = config.LocalModeArchive
	s.RemoveSourceFiles = &yes
	return s, config.Defaults{Retention: config.Retention{Recent: 5}}
}

// rsyncFake returns a handler that satisfies the preflight, transfer, and
// snapshot steps, recording the rsync transfer's argv into *got.
func rsyncFake(t *testing.T, gotArgs *[]string, sshErr error) func(string, []string) (execx.Result, error) {
	t.Helper()
	return func(name string, args []string) (execx.Result, error) {
		switch {
		case name == "rsync" && len(args) == 1 && args[0] == "--version":
			return execx.Result{Stdout: "rsync version 3"}, nil
		case name == "rsync":
			*gotArgs = args
			return execx.Result{Stdout: "Number of regular files transferred: 5\nTotal transferred file size: 42 bytes\n"}, nil
		case name == "ssh":
			if sshErr != nil {
				return execx.Result{Code: 1, Stderr: "boom"}, sshErr
			}
			return execx.Result{}, nil
		case name == "cp":
			return cpHardlinkFake(args)
		}
		return execx.Result{}, nil
	}
}

// End to end for the motivating case: no --delete on the local side, rsync's own
// --remove-source-files on the remote side, and a cleanup pass for the empty
// directories rsync leaves behind.
func TestSyncOneArchiveRemovesSourceAndPrunesRemoteDirs(t *testing.T) {
	s, d := archiveEntry(t)
	var rsyncArgs []string
	fr := &execx.FakeRunner{Handler: rsyncFake(t, &rsyncArgs, nil)}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}

	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("res = %+v", res)
	}
	j := strings.Join(rsyncArgs, " ")
	if strings.Contains(j, "--delete") {
		t.Fatalf("archive mode must not pass --delete: %s", j)
	}
	if !strings.Contains(j, "--remove-source-files") {
		t.Fatalf("missing --remove-source-files: %s", j)
	}
	if !calledRemotePrune(fr, "/mnt/sd/record") {
		t.Fatalf("remote empty-dir cleanup not run: %+v", fr.Calls)
	}
}

// The whole point of the guardrail's other half: without remove_source_files,
// nothing on the remote is touched.
func TestSyncOneArchiveWithoutRemoveSourceLeavesRemoteAlone(t *testing.T) {
	s := okEntry(t)
	s.LocalMode = config.LocalModeArchive
	var rsyncArgs []string
	fr := &execx.FakeRunner{Handler: rsyncFake(t, &rsyncArgs, nil)}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}

	res := SyncOne(context.Background(), s, config.Defaults{}, deps, false)
	if !res.OK {
		t.Fatalf("res = %+v", res)
	}
	j := strings.Join(rsyncArgs, " ")
	if strings.Contains(j, "--remove-source-files") {
		t.Fatalf("remove-source must stay off: %s", j)
	}
	if strings.Contains(j, "--delete") {
		t.Fatalf("archive must not pass --delete: %s", j)
	}
	if anySSHCall(fr) {
		t.Fatalf("no remote command should run: %+v", fr.Calls)
	}
}

// A dry run must delete nothing anywhere: rsync runs under -n (so its own
// --remove-source-files is inert), and the remote empty-dir cleanup — which is
// a real `find -delete` with no preview form — must not run at all.
func TestSyncOneArchiveDryRunDeletesNothing(t *testing.T) {
	s, d := archiveEntry(t)
	var rsyncArgs []string
	fr := &execx.FakeRunner{Handler: rsyncFake(t, &rsyncArgs, nil)}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}

	res := SyncOne(context.Background(), s, d, deps, true)
	if !res.OK {
		t.Fatalf("dry-run res = %+v", res)
	}
	if !contains(rsyncArgs, "-n") {
		t.Fatalf("dry-run rsync must pass -n so --remove-source-files is a no-op: %v", rsyncArgs)
	}
	if calledRemotePrune(fr, "/mnt/sd/record") {
		t.Fatalf("dry-run must not run the remote delete: %+v", fr.Calls)
	}
	// Belt and braces: no invocation of any kind may carry a delete verb.
	for _, c := range fr.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "-delete") && c.Name == "ssh" {
			t.Fatalf("dry-run issued a remote delete: %+v", c)
		}
	}
}

// A failed cleanup is a warning, not a failure: the data is already local, so an
// empty directory left on the remote must not fail the entry or lose the
// snapshot.
func TestSyncOneRemotePruneFailureDoesNotFailEntry(t *testing.T) {
	s, d := archiveEntry(t)
	var rsyncArgs []string
	fr := &execx.FakeRunner{Handler: rsyncFake(t, &rsyncArgs, errors.New("ssh exploded"))}
	log := &captureLog{}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: log,
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}

	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("cleanup failure must not fail the entry: %+v", res)
	}
	if res.Snapshot == "" {
		t.Fatal("snapshot must still be created")
	}
	if !strings.Contains(strings.Join(log.lines, "\n"), "empty-dir cleanup failed") {
		t.Fatalf("failure not surfaced in the log: %v", log.lines)
	}
}

// On a partial transfer (rsync 23/24) the backup still completes, but the
// remote is in an unexpected state, so the deletion pass is deferred to the next
// clean run.
func TestSyncOnePartialTransferSkipsRemotePrune(t *testing.T) {
	s, d := archiveEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		switch {
		case name == "rsync" && len(args) == 1 && args[0] == "--version":
			return execx.Result{Stdout: "rsync version 3"}, nil
		case name == "rsync":
			return execx.Result{
				Stdout: "Number of regular files transferred: 5\n",
				Code:   24,
			}, errors.New("partial transfer due to vanished source files")
		case name == "cp":
			return cpHardlinkFake(args)
		}
		return execx.Result{}, nil
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}

	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK {
		t.Fatalf("exit 24 should still complete the backup: %+v", res)
	}
	if calledRemotePrune(fr, "/mnt/sd/record") {
		t.Fatalf("partial transfer must skip the remote delete: %+v", fr.Calls)
	}
}

// Mirror mode is untouched by any of this: --delete stays, and no remote
// command runs.
func TestSyncOneMirrorUnchanged(t *testing.T) {
	s := okEntry(t)
	var rsyncArgs []string
	fr := &execx.FakeRunner{Handler: rsyncFake(t, &rsyncArgs, nil)}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}

	if res := SyncOne(context.Background(), s, config.Defaults{}, deps, false); !res.OK {
		t.Fatalf("res = %+v", res)
	}
	if !contains(rsyncArgs, "--delete") {
		t.Fatalf("mirror mode must keep --delete: %v", rsyncArgs)
	}
	if anySSHCall(fr) {
		t.Fatalf("mirror mode must not run remote commands: %+v", fr.Calls)
	}
}

// Entry-level settings may also be inherited from [defaults].
func TestSyncOneArchiveInheritedFromDefaults(t *testing.T) {
	s := okEntry(t)
	s.RemotePath = "/mnt/sd/record"
	d := config.Defaults{LocalMode: config.LocalModeArchive, RemoveSourceFiles: true}
	var rsyncArgs []string
	fr := &execx.FakeRunner{Handler: rsyncFake(t, &rsyncArgs, nil)}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}

	if res := SyncOne(context.Background(), s, d, deps, false); !res.OK {
		t.Fatalf("res = %+v", res)
	}
	j := strings.Join(rsyncArgs, " ")
	if strings.Contains(j, "--delete") || !strings.Contains(j, "--remove-source-files") {
		t.Fatalf("defaults not inherited: %s", j)
	}
	if !calledRemotePrune(fr, "/mnt/sd/record") {
		t.Fatalf("remote cleanup not run: %+v", fr.Calls)
	}
}

// calledRemotePrune reports whether an ssh call carrying the empty-dir cleanup
// for root was issued.
func calledRemotePrune(fr *execx.FakeRunner, root string) bool {
	for _, c := range fr.Calls {
		if c.Name != "ssh" {
			continue
		}
		j := strings.Join(c.Args, " ")
		if strings.Contains(j, "-empty") && strings.Contains(j, "-delete") && strings.Contains(j, root) {
			return true
		}
	}
	return false
}

func anySSHCall(fr *execx.FakeRunner) bool {
	for _, c := range fr.Calls {
		if c.Name == "ssh" {
			return true
		}
	}
	return false
}
