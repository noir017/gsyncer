// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

// Package schedule decides when an entry is due. It parses cron expressions,
// persists each entry's next-run state, and works out how an unreachable host
// moves that state forward: retry a little later, skip to the next scheduled
// occurrence, or give up on this one.
//
// It deliberately knows nothing about config or rsync — callers pass in the
// resolved expression and offline policy — so the state machine is plain
// functions over (State, time) and is testable without a filesystem or ssh.
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Spec is a parsed schedule expression.
type Spec struct {
	expr  string
	sched cron.Schedule
}

// Parse accepts a standard 5-field cron expression (minute hour dom month dow,
// evaluated in local time) or a descriptor such as "@daily". An expression that
// parses but can never fire (e.g. "0 0 30 2 *") is rejected too: it would load
// fine and then silently never back anything up.
func Parse(expr string) (Spec, error) {
	e := strings.TrimSpace(expr)
	s, err := cron.ParseStandard(e)
	if err != nil {
		return Spec{}, fmt.Errorf("invalid schedule %q: %w", expr, err)
	}
	if s.Next(time.Now()).IsZero() {
		return Spec{}, fmt.Errorf("schedule %q never fires", expr)
	}
	return Spec{expr: e, sched: s}, nil
}

// Next returns the first scheduled time strictly after t.
func (s Spec) Next(t time.Time) time.Time { return s.sched.Next(t) }

// String returns the expression the Spec was parsed from.
func (s Spec) String() string { return s.expr }

// Values recorded in State.LastResult.
const (
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultOffline = "offline"         // host unreachable; a retry is pending
	ResultSkipped = "offline-skipped" // host unreachable; occurrence skipped
	ResultGaveUp  = "offline-gave-up" // host unreachable; retries exhausted
)

// State is one entry's persisted scheduling state.
type State struct {
	// Schedule is the expression NextRun was computed from. When the configured
	// expression differs, NextRun is recomputed so an edit takes effect at once.
	Schedule string    `json:"schedule"`
	NextRun  time.Time `json:"next_run"`
	// Pending is the scheduled occurrence an offline retry is still trying to
	// serve; zero when no retry is outstanding. Attempts counts the offline
	// attempts made for it so far, the first one included.
	Pending  time.Time `json:"pending"`
	Attempts int       `json:"attempts"`

	LastRun    time.Time `json:"last_run"`
	LastResult string    `json:"last_result"`
	LastError  string    `json:"last_error"`
}

// Due reports whether the entry should run at now. The comparison is "next run
// is not in the future" rather than "now matches the expression", so a tick
// that was missed (host down, cron hiccup) still fires the occurrence late
// instead of losing it — exactly once, since a run moves NextRun past now.
func (st State) Due(now time.Time) bool {
	return !st.NextRun.IsZero() && !now.Before(st.NextRun)
}

// Prepare aligns st with the configured expression. With no recorded next run
// (a new entry) or a changed expression, NextRun is recomputed from now: a new
// entry waits for its first occurrence rather than firing the moment it is
// added, and an edited schedule takes effect immediately. The bool reports
// whether st changed and should be saved.
//
// It also repairs a NextRun that lies beyond the next occurrence from now. That
// cannot happen in normal operation — every NextRun is computed from a time no
// later than the present — so it means the clock was once ahead (an RTC that
// read 2030 until NTP corrected it). Left alone, the entry would silently never
// come due until that date.
func Prepare(st State, expr string, spec Spec, now time.Time) (State, bool) {
	next := spec.Next(now)
	if st.Schedule == expr && !st.NextRun.IsZero() && !st.NextRun.After(next) {
		return st, false
	}
	st.Schedule = expr
	st = settle(st, next)
	return st, true
}

// settle closes out the current occurrence and points at next.
func settle(st State, next time.Time) State {
	st.NextRun = next
	st.Pending = time.Time{}
	st.Attempts = 0
	return st
}

// AfterRun records a run that actually reached the host (whether the sync then
// succeeded or failed) and moves on to the next scheduled occurrence. A failed
// sync is not retried here: it has already alerted, and a host that answers but
// cannot be backed up needs a human, not a loop.
func AfterRun(st State, spec Spec, now time.Time, ok bool, errMsg string) State {
	st = settle(st, spec.Next(now))
	st.LastRun = now
	st.LastError = errMsg
	if ok {
		st.LastResult = ResultOK
	} else {
		st.LastResult = ResultFailed
	}
	return st
}

// Policy is an entry's resolved handling of an unreachable host. A "force"
// entry never probes, so it never reaches AfterOffline.
type Policy struct {
	Retry    bool          // true: retry after Interval; false: skip this occurrence
	Interval time.Duration // spacing between retries
	Limit    int           // max retries per occurrence; 0 = until the next occurrence
}

// Verdict is what AfterOffline decided.
type Verdict int

const (
	// Retry: another attempt is scheduled for the same occurrence.
	Retry Verdict = iota
	// Skip: the occurrence is dropped (policy says skip); nothing to report.
	Skip
	// GiveUp: retrying stopped without ever reaching the host — the limit was
	// hit, or the next retry would land on or after the next occurrence, which
	// takes over from here.
	GiveUp
)

// Offline is the outcome of one offline attempt.
type Offline struct {
	Verdict  Verdict
	Attempts int       // offline attempts for this occurrence, this one included
	NextRun  time.Time // when the entry will next be attempted
}

// AfterOffline records an attempt that found the host unreachable.
//
// Retries are spaced from the attempt's planned time, not from now: a tick
// fires a few seconds into its minute, and spacing from now would let a daily
// retry drift a minute later every day. Retries never run past the next
// scheduled occurrence — that occurrence is a fresh chance, and serving the
// old one on top of it would sync twice for nothing.
func AfterOffline(st State, spec Spec, now time.Time, p Policy, errMsg string) (State, Offline) {
	if !st.Pending.IsZero() && !now.Before(spec.Next(st.Pending)) {
		// The occurrence being chased was overtaken by a newer one while nothing
		// ran (the machine running gsyncer was down across it). The newer one
		// is what counts now, with a fresh attempt count and retries aligned to
		// its time.
		st.Pending = latestOccurrence(spec, st.Pending, now)
		st.NextRun = st.Pending
		st.Attempts = 0
	}
	occ := st.Pending
	if occ.IsZero() {
		occ = st.NextRun // first miss: the occurrence being served is the one that just came due
	}
	attempts := st.Attempts + 1
	next := spec.Next(now)
	st.LastRun = now
	st.LastError = errMsg

	if !p.Retry {
		st = settle(st, next)
		st.LastResult = ResultSkipped
		return st, Offline{Verdict: Skip, Attempts: attempts, NextRun: next}
	}

	retryAt := nextRetry(st.NextRun, now, p.Interval)
	if (p.Limit > 0 && attempts > p.Limit) || !retryAt.Before(next) {
		st = settle(st, next)
		st.LastResult = ResultGaveUp
		return st, Offline{Verdict: GiveUp, Attempts: attempts, NextRun: next}
	}
	st.Pending = occ
	st.Attempts = attempts
	st.NextRun = retryAt
	st.LastResult = ResultOffline
	return st, Offline{Verdict: Retry, Attempts: attempts, NextRun: retryAt}
}

// latestOccurrence returns the last scheduled time in (from, now], or from if
// there is none.
func latestOccurrence(spec Spec, from, now time.Time) time.Time {
	t := from
	for n := spec.Next(t); !n.After(now); n = spec.Next(t) {
		t = n
	}
	return t
}

// nextRetry returns the first base+k*interval (k >= 1) strictly after now.
func nextRetry(base, now time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		interval = time.Minute // Validate forbids this; never loop on a zero step
	}
	if base.IsZero() || base.After(now) {
		base = now
	}
	k := now.Sub(base)/interval + 1
	return base.Add(k * interval)
}

// Resolve settles an outstanding offline retry after a successful manual sync:
// the occurrence it was chasing has now been served, so the entry goes back to
// waiting for its next scheduled time. Returns false when nothing was pending.
func Resolve(st State, spec Spec, now time.Time) (State, bool) {
	if st.Pending.IsZero() {
		return st, false
	}
	st = settle(st, spec.Next(now))
	st.LastRun = now
	st.LastResult = ResultOK
	st.LastError = ""
	return st, true
}
