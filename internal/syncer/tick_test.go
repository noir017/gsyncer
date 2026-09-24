// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/schedule"
)

// Real stderr captured on the deployment (unraid → Teleport / plain ssh).
const (
	errTeleportOffline = "ERROR: failed connecting to host pacvue: target host pacvue is offline or does not exist\n\nConnection closed by UNKNOWN port 65535"
	errNoRoute         = "ssh: connect to host 192.168.0.254 port 22: No route to host"
	errRefused         = "ssh: connect to host 192.168.0.10 port 1: Connection refused"
	errDNS             = "ssh: Could not resolve hostname no-such-host.invalid: Name or service not known"
	errDenied          = "root@pacvue.teleport-unraid.lan.noharanas.eu.org: Permission denied (publickey)."
	errHostKey         = "@@@@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\nHost key verification failed."
)

func sshFail(stderr string) (execx.Result, error) {
	return execx.Result{Code: 255, Stderr: stderr}, errors.New("exit status 255")
}

func TestClassifyProbe(t *testing.T) {
	cases := []struct {
		name            string
		res             execx.Result
		err             error
		timedOut        bool
		online, offline bool
	}{
		{"reachable", execx.Result{}, nil, false, true, false},
		{"windows shell without true", execx.Result{Code: 1, Stderr: "CommandNotFoundException"}, errors.New("exit status 1"), false, true, false},
		{"teleport node offline", execx.Result{Code: 255, Stderr: errTeleportOffline}, errors.New("x"), false, false, true},
		{"no route", execx.Result{Code: 255, Stderr: errNoRoute}, errors.New("x"), false, false, true},
		{"refused", execx.Result{Code: 255, Stderr: errRefused}, errors.New("x"), false, false, true},
		{"dns", execx.Result{Code: 255, Stderr: errDNS}, errors.New("x"), false, false, true},
		{"probe timeout", execx.Result{Code: -1}, errors.New("signal: killed"), true, false, true},
		{"key rejected is not offline", execx.Result{Code: 255, Stderr: errDenied}, errors.New("x"), false, false, false},
		{"host key changed is not offline", execx.Result{Code: 255, Stderr: errHostKey}, errors.New("x"), false, false, false},
		{"unknown ssh failure is not offline", execx.Result{Code: 255, Stderr: "something new"}, errors.New("x"), false, false, false},
		{"ssh binary missing", execx.Result{}, errors.New(`exec: "ssh": executable file not found`), false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pr := classifyProbe(c.res, c.err, c.timedOut)
			if pr.Online != c.online || pr.Offline != c.offline {
				t.Fatalf("got online=%v offline=%v (%q), want online=%v offline=%v",
					pr.Online, pr.Offline, pr.Detail, c.online, c.offline)
			}
		})
	}
}

func noProbeDelay(t *testing.T) {
	t.Helper()
	old := probeRetryDelay
	probeRetryDelay = 0
	t.Cleanup(func() { probeRetryDelay = old })
}

func TestProbeRetriesOnceBeforeCallingOffline(t *testing.T) {
	noProbeDelay(t)
	s := config.Sync{Name: "l", Host: "tp-pacvue", User: "user", RemotePath: "/r", LocalPath: "/x"}

	calls := 0
	fr := &execx.FakeRunner{Handler: func(string, []string) (execx.Result, error) {
		calls++
		if calls == 1 {
			return sshFail(errTeleportOffline) // a blip
		}
		return execx.Result{}, nil
	}}
	if pr := Probe(context.Background(), s, config.Defaults{}, Deps{Runner: fr}); !pr.Online || calls != 2 {
		t.Fatalf("a single failed probe must be retried: %+v after %d calls", pr, calls)
	}

	fr = &execx.FakeRunner{Handler: func(string, []string) (execx.Result, error) { return sshFail(errTeleportOffline) }}
	if pr := Probe(context.Background(), s, config.Defaults{}, Deps{Runner: fr}); !pr.Offline || len(fr.Calls) != probeAttempts {
		t.Fatalf("want offline after %d probes, got %+v after %d", probeAttempts, pr, len(fr.Calls))
	}
	// The probe goes over the same user@host (and so the same ssh_config alias)
	// as the transfer.
	args := fr.Calls[0].Args
	if args[len(args)-2] != "user@tp-pacvue" || args[len(args)-1] != "true" {
		t.Fatalf("probe command = %v", args)
	}

	fr = &execx.FakeRunner{Handler: func(string, []string) (execx.Result, error) { return sshFail(errDenied) }}
	if pr := Probe(context.Background(), s, config.Defaults{}, Deps{Runner: fr}); pr.Online || pr.Offline || len(fr.Calls) != 1 {
		t.Fatalf("an auth failure is neither, and not retried: %+v after %d", pr, len(fr.Calls))
	}
}

// --- Tick ---

var cst = time.FixedZone("CST", 8*3600)

func cstAt(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cst)
	if err != nil {
		panic(err)
	}
	return t
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(s string)   { c.mu.Lock(); c.t = cstAt(s); c.mu.Unlock() }

// tickEnv is one scheduled entry with a scriptable host.
type tickEnv struct {
	t     *testing.T
	entry config.Sync
	store schedule.Store
	clock *fakeClock
	log   *captureLog

	mu     sync.Mutex
	probe  []func() (execx.Result, error) // consumed in order; the last one repeats
	rsync  func() (execx.Result, error)
	probes int
	syncs  int
}

func newTickEnv(t *testing.T, sched, onOffline string) *tickEnv {
	t.Helper()
	noProbeDelay(t)
	root := t.TempDir()
	e := &tickEnv{
		t: t,
		entry: config.Sync{
			Name: "pacvue-record", Host: "tp-pacvue", User: "user",
			RemotePath: "/mnt/c/liqin/record", LocalPath: filepath.Join(root, "backup"),
			Schedule: sched, OnOffline: onOffline, OfflineRetryInterval: "24h",
		},
		store: schedule.Store{Dir: filepath.Join(root, "state")},
		clock: &fakeClock{},
		log:   &captureLog{},
		rsync: func() (execx.Result, error) {
			return execx.Result{Stdout: "Number of regular files transferred: 3\n"}, nil
		},
	}
	e.hostOnline()
	return e
}

func (e *tickEnv) hostOnline() {
	e.probe = []func() (execx.Result, error){func() (execx.Result, error) { return execx.Result{}, nil }}
}
func (e *tickEnv) hostOffline() {
	e.probe = []func() (execx.Result, error){func() (execx.Result, error) { return sshFail(errTeleportOffline) }}
}

func (e *tickEnv) runner() *execx.FakeRunner {
	return &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch {
		case name == "ssh" && args[len(args)-1] == "true":
			e.probes++
			f := e.probe[0]
			if len(e.probe) > 1 {
				e.probe = e.probe[1:]
			}
			return f()
		case name == "rsync" && len(args) == 1 && args[0] == "--version":
			return execx.Result{Stdout: "rsync version 3"}, nil
		case name == "rsync":
			e.syncs++
			return e.rsync()
		case name == "cp":
			return cpHardlinkFake(args)
		}
		return execx.Result{}, nil
	}}
}

func (e *tickEnv) tick(now string, dryRun bool) TickResult {
	e.t.Helper()
	e.clock.Set(now)
	deps := Deps{Runner: e.runner(), FSType: ext4FS, Log: e.log, Now: e.clock.Now}
	rs := Tick(context.Background(), []config.Sync{e.entry}, config.Defaults{}, deps, e.store, dryRun, 1)
	return rs[0]
}

func (e *tickEnv) state() schedule.State {
	e.t.Helper()
	st, err := e.store.Load(e.entry.Name)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *tickEnv) setState(st schedule.State) {
	e.t.Helper()
	if st.Schedule == "" {
		st.Schedule = e.entry.Schedule
	}
	if err := e.store.Save(e.entry.Name, st); err != nil {
		e.t.Fatal(err)
	}
}

func TestTickNotDueTouchesNothing(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	r := e.tick("2026-09-24 15:00:00", false) // Thursday: first sight of the entry
	if r.Event != TickIdle || r.Reportable() {
		t.Fatalf("got %+v, want idle", r)
	}
	if e.probes+e.syncs != 0 || len(e.log.lines) != 0 {
		t.Fatalf("not-due tick must not probe, sync or log: probes=%d syncs=%d log=%v", e.probes, e.syncs, e.log.lines)
	}
	if _, err := os.Stat(e.entry.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("local_path must not be touched while not due (err=%v)", err)
	}
	if st := e.state(); !st.NextRun.Equal(cstAt("2026-09-28 16:00:00")) {
		t.Fatalf("first sight should record the next occurrence, got %v", st.NextRun)
	}
	if !r.NextRun.Equal(cstAt("2026-09-28 16:00:00")) {
		t.Fatalf("result NextRun = %v", r.NextRun)
	}
}

func TestTickManualOnlyEntryIsIgnored(t *testing.T) {
	e := newTickEnv(t, "", config.OfflineRetry)
	if r := e.tick("2026-09-28 16:00:00", false); r.Event != TickIdle {
		t.Fatalf("got %+v", r)
	}
	if _, err := os.Stat(e.store.Dir); !os.IsNotExist(err) {
		t.Fatal("a manual-only entry should not even get a state file")
	}
}

func TestTickForceSyncsWithoutProbing(t *testing.T) {
	e := newTickEnv(t, "5 3 * * 2", config.OfflineForce)
	e.setState(schedule.State{NextRun: cstAt("2026-09-29 03:05:00")})
	r := e.tick("2026-09-29 03:05:01", false)
	if r.Event != TickRan || !r.OK || r.Snapshot == "" {
		t.Fatalf("got %+v", r)
	}
	if e.probes != 0 || e.syncs != 1 {
		t.Fatalf("force must not probe: probes=%d syncs=%d", e.probes, e.syncs)
	}
	st := e.state()
	if !st.NextRun.Equal(cstAt("2026-10-06 03:05:00")) || st.LastResult != schedule.ResultOK {
		t.Fatalf("state after run: %+v", st)
	}
	// The same minute ticking again must not run twice.
	if r := e.tick("2026-09-29 03:05:40", false); r.Event != TickIdle || e.syncs != 1 {
		t.Fatalf("second tick in the same minute: %+v syncs=%d", r, e.syncs)
	}
}

func TestTickRetryThenRecover(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	e.setState(schedule.State{NextRun: cstAt("2026-09-28 16:00:00")})
	e.hostOffline()

	r := e.tick("2026-09-28 16:00:02", false)
	if r.Event != TickOfflineRetry || r.Attempts != 1 || !r.Reportable() || !r.Skipped {
		t.Fatalf("first miss: %+v", r)
	}
	if e.syncs != 0 {
		t.Fatal("offline host must not be synced")
	}
	if !r.NextRun.Equal(cstAt("2026-09-29 16:00:00")) {
		t.Fatalf("retry at %v, want Tue 16:00", r.NextRun)
	}
	logged := strings.Join(e.log.lines, "\n")
	if !strings.Contains(logged, "host offline (attempt 1)") || !strings.Contains(logged, "is offline or does not exist") {
		t.Fatalf("first miss should be logged with the probe output: %s", logged)
	}

	// Between retries nothing happens.
	if r := e.tick("2026-09-29 09:00:00", false); r.Event != TickIdle {
		t.Fatalf("before retry time: %+v", r)
	}

	e.log.lines = nil
	r = e.tick("2026-09-29 16:00:03", false)
	if r.Event != TickOfflineRetry || r.Attempts != 2 || r.Reportable() {
		t.Fatalf("second miss must be quiet: %+v", r)
	}
	if len(e.log.lines) != 0 {
		t.Fatalf("intermediate retries must not write the run log: %v", e.log.lines)
	}

	e.hostOnline()
	r = e.tick("2026-09-30 16:00:01", false)
	if r.Event != TickRan || !r.OK {
		t.Fatalf("host back: %+v", r)
	}
	st := e.state()
	if !st.NextRun.Equal(cstAt("2026-10-05 16:00:00")) || !st.Pending.IsZero() || st.Attempts != 0 {
		t.Fatalf("a success settles the occurrence: %+v", st)
	}
}

func TestTickSkipOffline(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineSkip)
	e.setState(schedule.State{NextRun: cstAt("2026-09-28 16:00:00")})
	e.hostOffline()
	r := e.tick("2026-09-28 16:00:02", false)
	if r.Event != TickOfflineSkip || !r.Skipped || !r.Reportable() {
		t.Fatalf("got %+v", r)
	}
	if st := e.state(); !st.NextRun.Equal(cstAt("2026-10-05 16:00:00")) || st.LastResult != schedule.ResultSkipped {
		t.Fatalf("skip waits for the next occurrence: %+v", st)
	}
}

func TestTickGiveUpIsAFailure(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	one := 1
	e.entry.OfflineRetryLimit = &one
	e.setState(schedule.State{NextRun: cstAt("2026-09-29 16:00:00"), Pending: cstAt("2026-09-28 16:00:00"), Attempts: 1})
	e.hostOffline()
	r := e.tick("2026-09-29 16:00:02", false)
	if r.Event != TickOfflineGaveUp || r.OK || r.Skipped || !r.Reportable() {
		t.Fatalf("got %+v", r)
	}
	if r.Err == nil || !strings.Contains(r.Err.Error(), "gave up after 2 attempt(s)") {
		t.Fatalf("error should say how many attempts: %v", r.Err)
	}
}

// A rejected key is not "offline": the sync runs, fails with rsync's real
// error, and the entry does not quietly retry.
func TestTickAuthFailureIsNotOffline(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	e.setState(schedule.State{NextRun: cstAt("2026-09-28 16:00:00")})
	e.probe = []func() (execx.Result, error){func() (execx.Result, error) { return sshFail(errDenied) }}
	e.rsync = func() (execx.Result, error) { return sshFail(errDenied) }
	r := e.tick("2026-09-28 16:00:02", false)
	if r.Event != TickRan || r.OK || r.Skipped {
		t.Fatalf("got %+v", r)
	}
	if st := e.state(); st.LastResult != schedule.ResultFailed || !st.Pending.IsZero() {
		t.Fatalf("state: %+v", st)
	}
}

func TestTickHostDropsOffMidSync(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	e.setState(schedule.State{NextRun: cstAt("2026-09-28 16:00:00")})
	ok := func() (execx.Result, error) { return execx.Result{}, nil }
	gone := func() (execx.Result, error) { return sshFail(errTeleportOffline) }
	e.probe = []func() (execx.Result, error){ok, gone} // up at the start, gone afterwards
	e.rsync = func() (execx.Result, error) {
		return execx.Result{Code: 255, Stderr: "Connection closed"}, errors.New("exit status 255")
	}
	r := e.tick("2026-09-28 16:00:02", false)
	if r.Event != TickOfflineRetry || !strings.Contains(r.Detail, "offline during sync") {
		t.Fatalf("a mid-transfer drop is an offline miss: %+v", r)
	}
	if st := e.state(); st.Pending.IsZero() || !st.NextRun.Equal(cstAt("2026-09-29 16:00:00")) {
		t.Fatalf("retry should be pending: %+v", st)
	}
}

func TestTickDryRunChangesNothing(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	due := schedule.State{Schedule: "0 16 * * 1", NextRun: cstAt("2026-09-28 16:00:00")}
	e.setState(due)

	if r := e.tick("2026-09-28 16:00:02", true); r.Event != TickWouldRun || e.syncs != 0 {
		t.Fatalf("online dry run: %+v syncs=%d", r, e.syncs)
	}
	e.hostOffline()
	if r := e.tick("2026-09-28 16:00:02", true); r.Event != TickOfflineRetry {
		t.Fatalf("offline dry run should preview the verdict: %+v", r)
	}
	if st := e.state(); !st.NextRun.Equal(due.NextRun) || st.Attempts != 0 {
		t.Fatalf("dry run must not change state: %+v", st)
	}
	if _, err := os.Stat(e.entry.LocalPath); !os.IsNotExist(err) {
		t.Fatal("dry run must not create local_path")
	}
}

func TestTickSkipsEntryHeldByAnotherTick(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineForce)
	e.setState(schedule.State{NextRun: cstAt("2026-09-28 16:00:00")})
	l, ok, err := e.store.Lock(e.entry.Name)
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer l.Release()
	if r := e.tick("2026-09-28 16:01:00", false); r.Event != TickIdle || e.syncs != 0 {
		t.Fatalf("held schedule lock: %+v syncs=%d", r, e.syncs)
	}
}

func TestTickWaitsForManualSync(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	e.setState(schedule.State{NextRun: cstAt("2026-09-28 16:00:00")})
	l, ok, err := acquireLock(e.entry.LocalPath) // a manual `gsyncer sync` in progress
	if err != nil || !ok {
		t.Fatal(err)
	}
	r := e.tick("2026-09-28 16:00:02", false)
	l.release()
	if r.Event != TickIdle || e.probes+e.syncs != 0 {
		t.Fatalf("got %+v probes=%d syncs=%d", r, e.probes, e.syncs)
	}
	if st := e.state(); !st.Due(cstAt("2026-09-28 16:01:00")) {
		t.Fatal("the entry must stay due for the next tick")
	}
}

// A state dir that cannot be written must stop the sync before it starts:
// otherwise the outcome could not be recorded, the entry would stay due, and
// every tick would sync (and snapshot, and prune) it again.
func TestTickUnwritableStateRefusesToSync(t *testing.T) {
	e := newTickEnv(t, "5 3 * * 2", config.OfflineForce)
	e.setState(schedule.State{NextRun: cstAt("2026-09-29 03:05:00")})
	// The lock file already exists from earlier ticks; only new files (the
	// state's temp file) cannot be created once the dir goes read-only.
	if err := os.WriteFile(filepath.Join(e.store.Dir, "pacvue-record.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.store.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(e.store.Dir, 0o700) })

	for _, now := range []string{"2026-09-29 03:05:01", "2026-09-29 03:06:01", "2026-09-29 03:07:01"} {
		r := e.tick(now, false)
		if r.Event != TickError || !strings.Contains(r.Err.Error(), "not starting the sync") {
			t.Fatalf("%s: got %+v", now, r)
		}
	}
	if e.syncs != 0 {
		t.Fatalf("an unwritable state dir must not lead to %d sync(s)", e.syncs)
	}
}

// Shutting down mid-transfer must not write the run off until next week.
func TestTickCancelledSyncStaysDue(t *testing.T) {
	e := newTickEnv(t, "5 3 * * 2", config.OfflineForce)
	e.setState(schedule.State{NextRun: cstAt("2026-09-29 03:05:00")})
	ctx, cancel := context.WithCancel(context.Background())
	e.rsync = func() (execx.Result, error) {
		cancel() // SIGTERM arrives during the transfer
		return execx.Result{Code: 20}, errors.New("signal: terminated")
	}
	e.clock.Set("2026-09-29 03:05:01")
	deps := Deps{Runner: e.runner(), FSType: ext4FS, Log: e.log, Now: e.clock.Now}
	r := Tick(ctx, []config.Sync{e.entry}, config.Defaults{}, deps, e.store, false, 1)[0]
	if r.Event != TickIdle || r.Reportable() {
		t.Fatalf("got %+v, want idle", r)
	}
	if st := e.state(); !st.NextRun.Equal(cstAt("2026-09-29 03:05:00")) || st.LastResult == schedule.ResultFailed {
		t.Fatalf("the interrupted run must stay due for catch-up: %+v", st)
	}
}

// With jobs=1, entries waiting for a slot must already be claimed, or the next
// minute's tick (another process) would start them alongside the first and a
// weekly batch would drift into running fully in parallel.
func TestTickClaimsQueuedEntriesBeforeRunning(t *testing.T) {
	root := t.TempDir()
	store := schedule.Store{Dir: filepath.Join(root, "state")}
	var entries []config.Sync
	for _, n := range []string{"a", "b", "c"} {
		entries = append(entries, config.Sync{Name: n, Host: "h", User: "u", RemotePath: "/r",
			LocalPath: filepath.Join(root, n), Schedule: "5 3 * * 2"})
		if err := store.Save(n, schedule.State{Schedule: "5 3 * * 2", NextRun: cstAt("2026-09-29 03:05:00")}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var queuedFree []string
	first := true
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		switch {
		case name == "rsync" && len(args) == 1:
			return execx.Result{Stdout: "rsync version 3"}, nil
		case name == "rsync":
			mu.Lock()
			if first {
				first = false
				// "a" is transferring; probe the queued entries' locks as a
				// second tick would.
				for _, n := range []string{"b", "c"} {
					if l, ok, _ := store.Lock(n); ok {
						queuedFree = append(queuedFree, n)
						l.Release()
					}
				}
			}
			mu.Unlock()
			return execx.Result{}, nil
		case name == "cp":
			return cpHardlinkFake(args)
		}
		return execx.Result{}, nil
	}}
	clock := &fakeClock{}
	clock.Set("2026-09-29 03:05:01")
	rs := Tick(context.Background(), entries, config.Defaults{},
		Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: clock.Now}, store, false, 1)
	if len(queuedFree) != 0 {
		t.Fatalf("queued entries %v were unclaimed while the first one ran", queuedFree)
	}
	for _, r := range rs {
		if r.Event != TickRan || !r.OK {
			t.Fatalf("%s: %+v", r.Name, r)
		}
	}
	for _, n := range []string{"a", "b", "c"} {
		l, ok, _ := store.Lock(n)
		if !ok {
			t.Fatalf("%s still locked after the tick", n)
		}
		l.Release()
	}
}

func TestSettleManualClearsPendingRetry(t *testing.T) {
	e := newTickEnv(t, "0 16 * * 1", config.OfflineRetry)
	e.setState(schedule.State{NextRun: cstAt("2026-09-29 16:00:00"), Pending: cstAt("2026-09-28 16:00:00"), Attempts: 1})
	results := []Result{{Name: e.entry.Name, OK: true}}
	if err := SettleManual(results, []config.Sync{e.entry}, config.Defaults{}, e.store, cstAt("2026-09-29 10:00:00")); err != nil {
		t.Fatal(err)
	}
	st := e.state()
	if !st.Pending.IsZero() || !st.NextRun.Equal(cstAt("2026-10-05 16:00:00")) {
		t.Fatalf("pending retry should be settled: %+v", st)
	}
}
