// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package syncer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"gsyncer/internal/config"
	"gsyncer/internal/schedule"
)

// HumanTime is the timestamp layout used in schedule log lines and summaries.
const HumanTime = "2006-01-02 15:04"

// TickEvent says what one tick did for one entry.
type TickEvent int

const (
	// TickIdle: nothing happened — not due, manual-only, or another process is
	// already working on the entry. Idle results are never logged or reported.
	TickIdle TickEvent = iota
	// TickRan: the sync ran; the embedded Result says how it went.
	TickRan
	// TickOfflineRetry: the host was unreachable; another attempt is scheduled.
	TickOfflineRetry
	// TickOfflineSkip: the host was unreachable; the occurrence was dropped.
	TickOfflineSkip
	// TickOfflineGaveUp: the host was unreachable and retrying stopped. It is
	// reported as a failure: the entry wanted this backup and did not get it.
	TickOfflineGaveUp
	// TickWouldRun: dry run only — the entry is due and its sync would start.
	TickWouldRun
	// TickError: scheduling itself failed (e.g. the state dir is unwritable).
	TickError
)

// TickResult is the outcome of one tick for one entry.
type TickResult struct {
	Result
	Event    TickEvent
	Attempts int       // offline attempts for the current occurrence (offline events)
	NextRun  time.Time // when the entry is next attempted; zero for manual-only entries
	Detail   string    // the probe output behind an offline event
}

// Reportable reports whether the result belongs in the run log and summary.
// Intermediate offline retries do not: with a short retry interval a laptop
// that is off all weekend would otherwise add hundreds of lines. The first
// miss of an occurrence and its final outcome tell the story; the attempts in
// between are visible in `gsyncer status`.
func (r TickResult) Reportable() bool {
	switch r.Event {
	case TickRan, TickOfflineSkip, TickOfflineGaveUp, TickWouldRun, TickError:
		return true
	case TickOfflineRetry:
		return r.Attempts == 1
	}
	return false
}

// Notifiable reports whether the result belongs in a run notification: runs
// that happened, and offline outcomes that mean a wanted backup did not.
// A skip or a pending retry is the configured behaviour working as intended,
// so it never turns into a notification — not even a "success" one for
// on_success subscribers.
func (r TickResult) Notifiable() bool {
	switch r.Event {
	case TickRan, TickOfflineGaveUp, TickError:
		return true
	}
	return false
}

// Tick runs every entry whose schedule has come due, applying its offline
// policy, with at most jobs entries in flight. It is meant to be called once a
// minute by cron.
//
// An entry that is not due costs one read of its state file and nothing else:
// its local_path is not touched, so an array disk holding the backups can stay
// spun down between runs.
//
// It works in two phases. First, sequentially, it claims every due entry by
// taking and holding its schedule lock. Only then do the claimed entries run,
// jobs at a time. Claiming up front matters: an entry still waiting for a free
// slot is already locked, so the next minute's tick cannot start it in a
// second process — which, a minute at a time, would quietly run a whole batch
// in parallel and make `jobs` meaningless.
func Tick(ctx context.Context, entries []config.Sync, d config.Defaults, deps Deps, store schedule.Store, dryRun bool, jobs int) []TickResult {
	results := make([]TickResult, len(entries))
	var claimed []*claim
	for i, s := range entries {
		var c *claim
		results[i], c = claimDue(s, d, deps, store, dryRun)
		if c != nil {
			c.idx = i
			claimed = append(claimed, c)
		}
	}

	if jobs < 1 {
		jobs = 1
	}
	runDeps := deps
	if jobs > 1 {
		runDeps.Log = &lockedLogger{mu: &sync.Mutex{}, inner: deps.Log}
	}
	var g errgroup.Group
	g.SetLimit(jobs)
	for _, c := range claimed {
		if ctx.Err() != nil {
			// Cancelled: leave the rest idle — their state still says due, so
			// the next tick picks them up.
			c.lock.Release()
			continue
		}
		c := c
		g.Go(func() error {
			defer c.lock.Release()
			results[c.idx] = runClaimed(ctx, c, d, runDeps, store, dryRun, results[c.idx])
			return nil
		})
	}
	_ = g.Wait()
	return results
}

// claim is a due entry whose schedule lock this tick holds.
type claim struct {
	idx  int
	s    config.Sync
	expr string
	spec schedule.Spec
	st   schedule.State
	lock *schedule.Lock
}

// claimDue decides whether s is due, and if so returns it claimed (lock held).
// Everything from reading the state to writing it back happens under that
// lock, so a tick that starts while the previous one is still on this entry
// sees it held and moves on, instead of reading state that is about to change
// and starting the same sync twice.
func claimDue(s config.Sync, d config.Defaults, deps Deps, store schedule.Store, dryRun bool) (TickResult, *claim) {
	tr := TickResult{Result: Result{Name: s.Name}}
	expr := s.EffectiveSchedule(d)
	if expr == "" {
		return tr, nil
	}
	spec, err := schedule.Parse(expr)
	if err != nil {
		// Validate rejects this at load; handled anyway so a bad expression can
		// never take down the other entries' tick.
		return tickFailed(tr, err), nil
	}
	lock, held, err := store.Lock(s.Name)
	if err != nil {
		return tickFailed(tr, fmt.Errorf("schedule lock: %w", err)), nil
	}
	if !held {
		return tr, nil
	}

	now := deps.Now()
	st, err := store.Load(s.Name)
	if err != nil {
		// A corrupt state file must not wedge the entry forever.
		deps.Log.Errorf("[%s] %v; restarting its schedule from now", s.Name, err)
		st = schedule.State{}
	}
	st, changed := schedule.Prepare(st, expr, spec, now)
	tr.NextRun = st.NextRun
	if !st.Due(now) {
		defer lock.Release()
		if changed && !dryRun {
			if err := store.Save(s.Name, st); err != nil {
				return tickFailed(tr, fmt.Errorf("save schedule state: %w", err)), nil
			}
		}
		return tr, nil
	}
	// A manual sync of this entry is running: leave the state due and let the
	// first tick after it finishes take over. Checked before probing so a long
	// manual run does not cost an ssh handshake every minute.
	if lockHeld(s.LocalPath) {
		lock.Release()
		return tr, nil
	}
	return tr, &claim{s: s, expr: expr, spec: spec, st: st, lock: lock}
}

// runClaimed probes (per the offline policy) and syncs one claimed entry, then
// records the outcome in its state.
func runClaimed(ctx context.Context, c *claim, d config.Defaults, deps Deps, store schedule.Store, dryRun bool, tr TickResult) TickResult {
	s, spec, st := c.s, c.spec, c.st
	probing := s.EffectiveOnOffline(d) != config.OfflineForce
	if probing {
		pr := Probe(ctx, s, d, deps)
		if ctx.Err() != nil {
			return tr
		}
		if pr.Offline {
			return tickOffline(tr, s, d, deps, store, spec, st, pr.Detail, dryRun)
		}
	}
	if dryRun {
		tr.Event = TickWouldRun
		tr.Skipped = true // a preview is neither a success nor a failure
		return tr
	}

	// Prove the state can be written before syncing. If it cannot, the outcome
	// could not be recorded either, the entry would still read as due, and
	// every tick would sync it again — each run a fresh snapshot, until
	// retention starts pruning real history to make room. Refusing up front
	// turns a broken state dir into a missed run instead of that loop.
	if err := store.Save(s.Name, st); err != nil {
		return tickFailed(tr, fmt.Errorf("schedule state is not writable, not starting the sync: %w", err))
	}

	deps.Log.Infof("[%s] scheduled run (%s, due %s)", s.Name, c.expr, st.NextRun.Format(HumanTime))
	res := SyncOne(ctx, s, d, deps, false)
	tr.Result = res
	if res.Skipped || (!res.OK && ctx.Err() != nil) {
		// Cancelled (shutdown mid-transfer), or a manual sync took local_path in
		// the meantime: the state stays due, so the first tick afterwards
		// catches the run up instead of writing it off until next time.
		tr.Event = TickIdle
		return tr
	}
	if !res.OK && probing && ctx.Err() == nil {
		// Did the host drop off mid-transfer (lid closed, VPN down)? Then this
		// is an offline miss rather than a broken backup, and the entry's
		// offline policy applies instead of a failure alert.
		if pr := Probe(ctx, s, d, deps); pr.Offline {
			deps.Log.Errorf("[%s] host went offline during the sync", s.Name)
			off := tickOffline(tr, s, d, deps, store, spec, st, "offline during sync: "+pr.Detail, false)
			off.Duration = res.Duration
			return off
		}
	}

	st = schedule.AfterRun(st, spec, deps.Now(), res.OK, errString(res.Err))
	tr.Event = TickRan
	tr.NextRun = st.NextRun
	if err := store.Save(s.Name, st); err != nil {
		// The state was writable a moment ago (see above), so this is a state
		// dir failing mid-run. The entry still reads as due; the next tick's
		// pre-sync save fails the same way and refuses to run it, so this costs
		// at most one repeat once the dir recovers. Make it loud regardless.
		deps.Log.Errorf("[%s] save schedule state: %v", s.Name, err)
		tr.OK = false
		tr.Err = fmt.Errorf("sync finished but its schedule state could not be saved: %w", err)
	}
	return tr
}

// tickOffline applies the entry's offline policy to an attempt that could not
// reach the host.
func tickOffline(tr TickResult, s config.Sync, d config.Defaults, deps Deps, store schedule.Store,
	spec schedule.Spec, st schedule.State, detail string, dryRun bool) TickResult {
	p := schedule.Policy{
		Retry:    s.EffectiveOnOffline(d) == config.OfflineRetry,
		Interval: s.EffectiveOfflineRetryInterval(d),
		Limit:    s.EffectiveOfflineRetryLimit(d),
	}
	st, o := schedule.AfterOffline(st, spec, deps.Now(), p, detail)
	tr.Result = Result{Name: s.Name}
	tr.Attempts, tr.NextRun, tr.Detail = o.Attempts, o.NextRun, detail
	switch o.Verdict {
	case schedule.Retry:
		tr.Event = TickOfflineRetry
		tr.Skipped = true
		tr.Err = fmt.Errorf("host offline; retry at %s", o.NextRun.Format(HumanTime))
	case schedule.Skip:
		tr.Event = TickOfflineSkip
		tr.Skipped = true
		tr.Err = fmt.Errorf("host offline; skipped this scheduled run")
	case schedule.GiveUp:
		tr.Event = TickOfflineGaveUp
		tr.Err = fmt.Errorf("host offline; gave up after %d attempt(s): %s", o.Attempts, detail)
	}
	if dryRun {
		return tr
	}
	if tr.Reportable() {
		switch o.Verdict {
		case schedule.Retry:
			deps.Log.Infof("[%s] host offline (attempt %d): %s; retrying at %s",
				s.Name, o.Attempts, detail, o.NextRun.Format(HumanTime))
		case schedule.Skip:
			deps.Log.Infof("[%s] host offline: %s; skipping this scheduled run, next at %s",
				s.Name, detail, o.NextRun.Format(HumanTime))
		case schedule.GiveUp:
			deps.Log.Errorf("[%s] host offline: %s; giving up after %d attempt(s), next scheduled run %s",
				s.Name, detail, o.Attempts, o.NextRun.Format(HumanTime))
		}
	}
	if err := store.Save(s.Name, st); err != nil {
		return tickFailed(tr, fmt.Errorf("save schedule state: %w", err))
	}
	return tr
}

// tickFailed marks tr as a scheduling error. It deliberately does not log: a
// broken state dir fails every tick, and the caller rate-limits how often that
// is reported — a log line here would still create a run log file a minute.
func tickFailed(tr TickResult, err error) TickResult {
	tr.Event = TickError
	tr.OK, tr.Skipped = false, false
	tr.Err = err
	return tr
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// SettleManual clears outstanding offline retries for entries a manual sync
// has just backed up, so a laptop synced by hand is not chased again for the
// same occurrence. An entry whose schedule lock is held is left alone — a tick
// is handling it. Errors are advisory: the backups themselves are done.
func SettleManual(results []Result, entries []config.Sync, d config.Defaults, store schedule.Store, now time.Time) error {
	byName := make(map[string]config.Sync, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	var errs []error
	for _, r := range results {
		s, ok := byName[r.Name]
		if !r.OK || !ok {
			continue
		}
		expr := s.EffectiveSchedule(d)
		if expr == "" {
			continue
		}
		spec, err := schedule.Parse(expr)
		if err != nil {
			continue
		}
		if err := settleOne(store, s.Name, expr, spec, now); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
		}
	}
	return errors.Join(errs...)
}

func settleOne(store schedule.Store, name, expr string, spec schedule.Spec, now time.Time) error {
	lock, held, err := store.Lock(name)
	if err != nil || !held {
		return err
	}
	defer lock.Release()
	st, err := store.Load(name)
	if err != nil {
		return err
	}
	if st.Schedule != expr {
		return nil // edited schedule: the next tick recomputes it anyway
	}
	if st, changed := schedule.Resolve(st, spec, now); changed {
		return store.Save(name, st)
	}
	return nil
}
