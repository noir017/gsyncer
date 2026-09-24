// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var cst = time.FixedZone("CST", 8*3600)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cst)
	if err != nil {
		panic(err)
	}
	return t
}

func mustParse(t *testing.T, expr string) Spec {
	t.Helper()
	sp, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return sp
}

func TestParse(t *testing.T) {
	for _, ok := range []string{"0 16 * * 1", "5 3 * * 2", "*/15 * * * *", "@daily", " 0 3 * * * "} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("Parse(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "0 16 * *", "61 * * * *", "0 25 * * *", "every monday", "0 0 30 2 *"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted, want error", bad)
		}
	}
}

func TestPrepareNewEntryWaitsForFirstOccurrence(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	now := at("2026-09-24 15:00:00") // Thursday
	st, changed := Prepare(State{}, "0 16 * * 1", sp, now)
	if !changed {
		t.Fatal("new entry should be initialised")
	}
	if want := at("2026-09-28 16:00:00"); !st.NextRun.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", st.NextRun, want)
	}
	if st.Due(now) {
		t.Fatal("a freshly added entry must not fire immediately")
	}
	// Unchanged expression: nothing to do.
	if _, changed := Prepare(st, "0 16 * * 1", sp, now.Add(time.Hour)); changed {
		t.Fatal("same expression should not reset state")
	}
}

func TestPrepareEditedScheduleRecomputes(t *testing.T) {
	old := State{Schedule: "5 3 * * 2", NextRun: at("2026-09-29 03:05:00"), Pending: at("2026-09-22 03:05:00"), Attempts: 2}
	sp := mustParse(t, "0 16 * * 1")
	st, changed := Prepare(old, "0 16 * * 1", sp, at("2026-09-24 15:00:00"))
	if !changed || st.Schedule != "0 16 * * 1" {
		t.Fatalf("expected recompute for edited schedule, got %+v", st)
	}
	if !st.NextRun.Equal(at("2026-09-28 16:00:00")) || !st.Pending.IsZero() || st.Attempts != 0 {
		t.Fatalf("edited schedule should start clean, got %+v", st)
	}
}

func TestDueCatchesUpMissedOccurrenceOnce(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	st := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-28 16:00:00")}
	// The host running gsyncer was down at 16:00 and came back the next morning.
	late := at("2026-09-29 09:12:00")
	if !st.Due(late) {
		t.Fatal("a missed occurrence must still fire late")
	}
	st = AfterRun(st, sp, late, true, "")
	if st.Due(late.Add(time.Minute)) {
		t.Fatal("the catch-up must happen exactly once")
	}
	if !st.NextRun.Equal(at("2026-10-05 16:00:00")) {
		t.Fatalf("NextRun = %v", st.NextRun)
	}
}

func TestAfterRunRecordsResult(t *testing.T) {
	sp := mustParse(t, "5 3 * * 2")
	st := State{Schedule: "5 3 * * 2", NextRun: at("2026-09-29 03:05:00")}
	st = AfterRun(st, sp, at("2026-09-29 03:05:02"), false, "rsync failed")
	if st.LastResult != ResultFailed || st.LastError != "rsync failed" {
		t.Fatalf("failed run not recorded: %+v", st)
	}
	if !st.NextRun.Equal(at("2026-10-06 03:05:00")) {
		t.Fatalf("a failed run waits for the next occurrence, got %v", st.NextRun)
	}
}

// The motivating case: weekly on Monday 16:00, retry every 24h while the laptop
// is off. Retries stay aligned to 16:00 (ticks fire a few seconds late) and the
// occurrence is abandoned on Sunday, when the next retry would collide with the
// following Monday.
func TestAfterOfflineDailyRetryThroughTheWeek(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	p := Policy{Retry: true, Interval: 24 * time.Hour}
	st := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-28 16:00:00")}

	days := []string{"2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02", "2026-10-03"}
	for i, d := range days {
		now := at(d + " 16:00:07")
		if !st.Due(now) {
			t.Fatalf("attempt %d on %s not due (NextRun %v)", i+1, d, st.NextRun)
		}
		var o Offline
		st, o = AfterOffline(st, sp, now, p, "offline")
		if o.Verdict != Retry || o.Attempts != i+1 {
			t.Fatalf("%s: got %+v, want retry attempt %d", d, o, i+1)
		}
		want := at(d + " 16:00:00").Add(24 * time.Hour)
		if !st.NextRun.Equal(want) || !o.NextRun.Equal(want) {
			t.Fatalf("%s: retry at %v, want %v (no drift)", d, st.NextRun, want)
		}
		if !st.Pending.Equal(at("2026-09-28 16:00:00")) || st.LastResult != ResultOffline {
			t.Fatalf("%s: pending occurrence not tracked: %+v", d, st)
		}
	}
	// Sunday: one more day would be Monday 16:00 — the next occurrence takes over.
	st, o := AfterOffline(st, sp, at("2026-10-04 16:00:05"), p, "offline")
	if o.Verdict != GiveUp || o.Attempts != 7 {
		t.Fatalf("Sunday: got %+v, want give-up after 7 attempts", o)
	}
	if !st.NextRun.Equal(at("2026-10-05 16:00:00")) || !st.Pending.IsZero() || st.Attempts != 0 {
		t.Fatalf("after giving up the entry waits for the next occurrence: %+v", st)
	}
	if st.LastResult != ResultGaveUp {
		t.Fatalf("LastResult = %q", st.LastResult)
	}
}

func TestAfterOfflineRetryLimit(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	p := Policy{Retry: true, Interval: 30 * time.Minute, Limit: 2}
	st := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-28 16:00:00")}
	want := []Verdict{Retry, Retry, GiveUp} // first attempt + 2 retries
	now := at("2026-09-28 16:00:03")
	for i, w := range want {
		var o Offline
		st, o = AfterOffline(st, sp, now, p, "offline")
		if o.Verdict != w {
			t.Fatalf("attempt %d: verdict %v, want %v", i+1, o.Verdict, w)
		}
		now = st.NextRun.Add(3 * time.Second)
	}
}

func TestAfterOfflineSkip(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	st := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-28 16:00:00")}
	st, o := AfterOffline(st, sp, at("2026-09-28 16:00:31"), Policy{}, "no route to host")
	if o.Verdict != Skip {
		t.Fatalf("verdict %v, want skip", o.Verdict)
	}
	if !st.NextRun.Equal(at("2026-10-05 16:00:00")) || st.LastResult != ResultSkipped || st.LastError != "no route to host" {
		t.Fatalf("skip should wait for the next occurrence: %+v", st)
	}
}

// After a long outage the retry grid is re-anchored past now instead of
// firing a burst of stale retries one tick after another.
func TestAfterOfflineRetryAfterOutageLandsInFuture(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	st := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-28 16:00:00")}
	now := at("2026-09-28 19:10:00")
	st, o := AfterOffline(st, sp, now, Policy{Retry: true, Interval: time.Hour}, "")
	if o.Verdict != Retry || !st.NextRun.Equal(at("2026-09-28 20:00:00")) {
		t.Fatalf("got %+v next %v, want retry at 20:00", o, st.NextRun)
	}
}

// A retry still chasing an occurrence that a newer one has overtaken (the NAS
// was down across it) restarts on the newer occurrence with a fresh count, so
// the old one's attempts do not eat into offline_retry_limit.
func TestAfterOfflineOvertakenOccurrenceStartsFresh(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	st := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-29 16:00:00"),
		Pending: at("2026-09-28 16:00:00"), Attempts: 1}
	now := at("2026-10-07 09:00:00") // NAS back after being off across Mon 10-05
	st, o := AfterOffline(st, sp, now, Policy{Retry: true, Interval: 24 * time.Hour}, "offline")
	if o.Verdict != Retry || o.Attempts != 1 {
		t.Fatalf("got %+v, want a first attempt at the newer occurrence", o)
	}
	if !st.Pending.Equal(at("2026-10-05 16:00:00")) {
		t.Fatalf("Pending = %v, want the 10-05 occurrence", st.Pending)
	}
	if !st.NextRun.Equal(at("2026-10-07 16:00:00")) {
		t.Fatalf("retry at %v, want 10-07 16:00 (aligned to the new occurrence)", st.NextRun)
	}
}

// A NextRun beyond the next occurrence can only come from a clock that was
// ahead; without repair the entry would never come due until that date.
func TestPrepareRepairsNextRunFromAFutureClock(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	st := State{Schedule: "0 16 * * 1", NextRun: at("2030-01-07 16:00:00")}
	st, changed := Prepare(st, "0 16 * * 1", sp, at("2026-09-24 15:00:00"))
	if !changed || !st.NextRun.Equal(at("2026-09-28 16:00:00")) {
		t.Fatalf("got %v changed=%v, want 2026-09-28 16:00", st.NextRun, changed)
	}
	// A pending retry (before the next occurrence) is normal and left alone.
	ok := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-25 16:00:00"), Pending: at("2026-09-21 16:00:00"), Attempts: 3}
	if _, changed := Prepare(ok, "0 16 * * 1", sp, at("2026-09-24 17:00:00")); changed {
		t.Fatal("a pending retry must not be reset")
	}
}

func TestResolveAfterManualSync(t *testing.T) {
	sp := mustParse(t, "0 16 * * 1")
	st := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-29 16:00:00"),
		Pending: at("2026-09-28 16:00:00"), Attempts: 1, LastResult: ResultOffline}
	st, changed := Resolve(st, sp, at("2026-09-29 10:00:00"))
	if !changed || !st.Pending.IsZero() || st.LastResult != ResultOK {
		t.Fatalf("pending retry should be settled: %+v", st)
	}
	if !st.NextRun.Equal(at("2026-10-05 16:00:00")) {
		t.Fatalf("NextRun = %v", st.NextRun)
	}
	if _, changed := Resolve(st, sp, at("2026-09-29 11:00:00")); changed {
		t.Fatal("nothing pending: Resolve must be a no-op")
	}
}

func TestStoreRoundTripAndMissing(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "state")}
	st, err := s.Load("web")
	if err != nil || st != (State{}) {
		t.Fatalf("missing state should be zero, got %+v %v", st, err)
	}
	want := State{Schedule: "0 16 * * 1", NextRun: at("2026-09-28 16:00:00"), Attempts: 2, LastResult: ResultOffline}
	if err := s.Save("web", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("web")
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextRun.Equal(want.NextRun) || got.Attempts != 2 || got.Schedule != want.Schedule {
		t.Fatalf("round trip: got %+v", got)
	}
	info, err := os.Stat(s.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir should be private, got %v %v", info.Mode(), err)
	}
}

func TestStoreCorruptIsError(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, "web.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("web"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("want corrupt error, got %v", err)
	}
}

func TestFileKeyIsSafeAndDistinct(t *testing.T) {
	cases := map[string]string{
		"pacvue-record": "pacvue-record",
		"../etc":        "%2E%2E%2Fetc",
		"a.b":           "a%2Eb",
		"a%2Eb":         "a%252Eb",
	}
	for in, want := range cases {
		if got := fileKey(in); got != want {
			t.Errorf("fileKey(%q) = %q, want %q", in, got, want)
		}
	}
	if fileKey("录像") == "" || strings.ContainsAny(fileKey("录像/x"), "/.") {
		t.Errorf("non-ASCII names must stay safe: %q", fileKey("录像/x"))
	}
}

func TestLockExcludesSecondHolder(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	l1, ok, err := s.Lock("web")
	if err != nil || !ok {
		t.Fatalf("first lock: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Lock("web"); err != nil || ok {
		t.Fatalf("second lock should be refused: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := s.Lock("other"); !ok {
		t.Fatal("locks are per entry")
	}
	l1.Release()
	l2, ok, err := s.Lock("web")
	if err != nil || !ok {
		t.Fatalf("lock after release: ok=%v err=%v", ok, err)
	}
	l2.Release()
}
