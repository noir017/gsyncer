package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
)

// baseHandler returns a FakeRunner handler that makes the happy path succeed.
func baseHandler(t *testing.T) func(name string, args []string) (execx.Result, error) {
	t.Helper()
	return func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 && args[0] == "--version" {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "ssh" {
			return execx.Result{}, nil
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 1\n"}, nil
		}
		if name == "cp" {
			return cpHardlinkFake(args)
		}
		return execx.Result{}, nil
	}
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }
}

// A successful pre_sync runs before rsync and post_sync after the snapshot,
// each with the expected GSYNC_* environment.
func TestSyncOneRunsHooks(t *testing.T) {
	s := okEntry(t)
	s.PreSync = "echo pre"
	s.PostSync = "echo post"
	fr := &execx.FakeRunner{Handler: baseHandler(t)}
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: fixedClock()}
	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("res = %+v", res)
	}

	var pre, post *execx.Call
	rsyncSeen := false
	for i := range fr.Calls {
		c := &fr.Calls[i]
		if c.Name == "sh" && len(c.Args) == 2 && c.Args[1] == "echo pre" {
			pre = c
			if rsyncSeen {
				t.Fatal("pre_sync must run before rsync")
			}
		}
		if c.Name == "rsync" && len(c.Args) > 1 {
			rsyncSeen = true
		}
		if c.Name == "sh" && len(c.Args) == 2 && c.Args[1] == "echo post" {
			post = c
			if !rsyncSeen {
				t.Fatal("post_sync must run after rsync")
			}
		}
	}
	if pre == nil || post == nil {
		t.Fatalf("hooks not run: pre=%v post=%v", pre, post)
	}
	preEnv := strings.Join(pre.Env, "\n")
	if !strings.Contains(preEnv, "GSYNC_PHASE=pre_sync") || !strings.Contains(preEnv, "GSYNC_NAME=web") {
		t.Fatalf("pre env missing keys: %v", pre.Env)
	}
	postEnv := strings.Join(post.Env, "\n")
	if !strings.Contains(postEnv, "GSYNC_PHASE=post_sync") ||
		!strings.Contains(postEnv, "GSYNC_SNAPSHOT=") ||
		!strings.Contains(postEnv, "GSYNC_FILES=1") {
		t.Fatalf("post env missing keys: %v", post.Env)
	}
}

// A failing pre_sync aborts the entry before rsync so no torn backup is made.
func TestSyncOnePreHookFailureSkipsRsync(t *testing.T) {
	s := okEntry(t)
	s.PreSync = "false"
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "sh" {
			return execx.Result{Code: 1}, errors.New("pre failed")
		}
		return baseHandler(t)(name, args)
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: fixedClock()}
	res := SyncOne(context.Background(), s, config.Defaults{}, deps, false)
	if res.OK || res.Err == nil {
		t.Fatalf("pre-hook failure should fail entry, got %+v", res)
	}
	for _, c := range fr.Calls {
		if c.Name == "rsync" || c.Name == "ssh" {
			t.Fatalf("no rsync/ssh should run after pre-hook failure, got %+v", c)
		}
	}
	if _, err := os.Stat(filepath.Join(s.LocalPath, "snapshots", "2026-06-24_030000")); err == nil {
		t.Fatal("no snapshot should be created after pre-hook failure")
	}
}

// A failing post_sync only warns: the backup already exists, so OK stays true.
func TestSyncOnePostHookFailureIsWarning(t *testing.T) {
	s := okEntry(t)
	s.PostSync = "false"
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "sh" {
			return execx.Result{Code: 1}, errors.New("post failed")
		}
		return baseHandler(t)(name, args)
	}}
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: fixedClock()}
	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("post-hook failure should not fail entry, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(s.LocalPath, "snapshots", "2026-06-24_030000")); err != nil {
		t.Fatalf("snapshot should still exist: %v", err)
	}
}

// Dry-run must not run side-effecting hooks.
func TestSyncOneDryRunSkipsHooks(t *testing.T) {
	s := okEntry(t)
	s.PreSync = "echo pre"
	s.PostSync = "echo post"
	fr := &execx.FakeRunner{Handler: baseHandler(t)}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: fixedClock()}
	res := SyncOne(context.Background(), s, config.Defaults{}, deps, true)
	if !res.OK {
		t.Fatalf("dry-run res = %+v", res)
	}
	for _, c := range fr.Calls {
		if c.Name == "sh" {
			t.Fatalf("dry-run must not run hooks, got %+v", c)
		}
	}
}

func TestEffectiveHooks(t *testing.T) {
	d := config.Defaults{PreSync: "d-pre", PostSync: "d-post"}
	if got := (config.Sync{}).EffectivePreSync(d); got != "d-pre" {
		t.Fatalf("inherit pre: %q", got)
	}
	if got := (config.Sync{PreSync: "e-pre"}).EffectivePreSync(d); got != "e-pre" {
		t.Fatalf("override pre: %q", got)
	}
	if got := (config.Sync{}).EffectivePostSync(d); got != "d-post" {
		t.Fatalf("inherit post: %q", got)
	}
}
