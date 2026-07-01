package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
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

// writeStatusTable emits an aligned human-readable table.
func writeStatusTable(w io.Writer, sts []EntryStatus) {
	fmt.Fprintf(w, "%-20s %-9s %6s  %-19s %10s  %s\n", "NAME", "BACKEND", "COUNT", "LATEST", "AGE(h)", "STALE")
	for _, s := range sts {
		latest := s.Latest
		if latest == "" {
			latest = "-"
		}
		stale := ""
		if s.Stale {
			stale = "STALE"
		}
		fmt.Fprintf(w, "%-20s %-9s %6d  %-19s %10.1f  %s\n",
			s.Name, s.Backend, s.Count, latest, s.AgeHours, stale)
	}
}
