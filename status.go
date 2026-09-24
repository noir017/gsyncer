// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/schedule"
	"gsyncer/internal/syncer"
)

// EntryStatus is the per-entry health derived from on-disk snapshots. It is the
// authoritative signal for "did backups keep happening" — unlike the batch-level
// run log, it is per entry and survives independent of any run record.
type EntryStatus struct {
	Name     string  `json:"name"`
	Backend  string  `json:"backend"`
	Count    int     `json:"count"`
	Latest   string  `json:"latest,omitempty"` // TSLayout, empty if no snapshots
	AgeHours float64 `json:"age_hours"`        // age of newest snapshot; 0 if none
	Stale    bool    `json:"stale"`

	// Scheduling, from the entry's config and state file; all empty for a
	// manual-only entry.
	Schedule        string `json:"schedule,omitempty"`
	OnOffline       string `json:"on_offline,omitempty"`
	NextRun         string `json:"next_run,omitempty"`         // syncer.HumanTime, local time
	OfflineAttempts int    `json:"offline_attempts,omitempty"` // >0 while an offline retry is pending
	LastResult      string `json:"last_result,omitempty"`      // schedule.Result* of the last scheduled attempt
	Overdue         bool   `json:"overdue,omitempty"`          // next run long past: tick not running, or a run in progress
}

// computeStatus derives one entry's status from its snapshot timestamps. When
// staleHours > 0 an entry is stale if its newest snapshot is older than that,
// or if it has no snapshots at all (a backup that never ran is the worst case).
func computeStatus(name, backend string, times []time.Time, now time.Time, staleHours float64) EntryStatus {
	st := EntryStatus{Name: name, Backend: backend, Count: len(times)}
	if len(times) == 0 {
		st.Stale = staleHours > 0
		return st
	}
	newest := times[0]
	for _, t := range times {
		if t.After(newest) {
			newest = t
		}
	}
	st.Latest = newest.Format("2006-01-02_150405")
	age := now.Sub(newest)
	st.AgeHours = age.Hours()
	if staleHours > 0 && age > time.Duration(staleHours*float64(time.Hour)) {
		st.Stale = true
	}
	return st
}

// writeStatusJSON emits the statuses as a JSON array.
func writeStatusJSON(w io.Writer, sts []EntryStatus) {
	b, _ := json.MarshalIndent(sts, "", "  ")
	fmt.Fprintln(w, string(b))
}

// writeStatusTable emits an aligned human-readable table. The NEXT and NOTE
// columns appear only when some entry is scheduled, so the output of a
// manual-only config — and any script parsing it — is unchanged.
func writeStatusTable(w io.Writer, sts []EntryStatus) {
	scheduled := false
	for _, s := range sts {
		scheduled = scheduled || s.Schedule != ""
	}
	if scheduled {
		fmt.Fprintf(w, "%-20s %-9s %6s  %-19s %10s  %-5s  %-16s  %s\n",
			"NAME", "BACKEND", "COUNT", "LATEST", "AGE(h)", "STALE", "NEXT", "NOTE")
	} else {
		fmt.Fprintf(w, "%-20s %-9s %6s  %-19s %10s  %s\n", "NAME", "BACKEND", "COUNT", "LATEST", "AGE(h)", "STALE")
	}
	for _, s := range sts {
		latest := s.Latest
		if latest == "" {
			latest = "-"
		}
		stale := ""
		if s.Stale {
			stale = "STALE"
		}
		if !scheduled {
			fmt.Fprintf(w, "%-20s %-9s %6d  %-19s %10.1f  %s\n",
				s.Name, s.Backend, s.Count, latest, s.AgeHours, stale)
			continue
		}
		next := s.NextRun
		if s.Schedule == "" {
			next = "manual"
		}
		fmt.Fprintf(w, "%-20s %-9s %6d  %-19s %10.1f  %-5s  %-16s  %s\n",
			s.Name, s.Backend, s.Count, latest, s.AgeHours, stale, next, statusNote(s))
	}
}

// statusNote summarises the scheduling state worth a human's attention.
func statusNote(s EntryStatus) string {
	var notes []string
	if s.OfflineAttempts > 0 {
		notes = append(notes, fmt.Sprintf("离线重试中（已尝试 %d 次）", s.OfflineAttempts))
	}
	switch s.LastResult {
	case schedule.ResultFailed:
		notes = append(notes, "上次排程运行失败")
	case schedule.ResultGaveUp:
		notes = append(notes, "上次因离线放弃")
	case schedule.ResultSkipped:
		notes = append(notes, "上次因离线跳过")
	}
	if s.Overdue {
		notes = append(notes, "已逾期（同步进行中，或 tick 没在运行）")
	}
	return strings.Join(notes, "；")
}

// overdueGrace is how late a next run may be before status calls it overdue.
// A tick fires every minute, so anything past a few minutes means either a run
// is still going or nothing is calling tick at all.
const overdueGrace = 5 * time.Minute

// applySchedule fills st's scheduling fields from the entry's config and saved
// state. A new or re-scheduled entry is previewed as the next tick would see
// it; nothing is written.
func applySchedule(st *EntryStatus, s config.Sync, d config.Defaults, store schedule.Store, now time.Time) {
	expr := s.EffectiveSchedule(d)
	if expr == "" {
		return
	}
	st.Schedule = expr
	st.OnOffline = s.EffectiveOnOffline(d)
	spec, err := schedule.Parse(expr)
	if err != nil {
		return
	}
	ss, err := store.Load(s.Name)
	if err != nil {
		ss = schedule.State{} // corrupt: tick resets it the same way
	}
	ss, _ = schedule.Prepare(ss, expr, spec, now)
	st.NextRun = ss.NextRun.Format(syncer.HumanTime)
	st.OfflineAttempts = ss.Attempts
	st.LastResult = ss.LastResult
	st.Overdue = now.Sub(ss.NextRun) > overdueGrace
}
