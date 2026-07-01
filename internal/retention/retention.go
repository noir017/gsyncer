// Package retention implements grandfather-father-son snapshot retention.
package retention

import (
	"sort"
	"time"
)

// Policy is the number of snapshots to keep in each layer.
type Policy struct {
	Recent     int
	Monthly    int
	Semiannual int
	Yearly     int
}

// Select returns the snapshots to KEEP, sorted newest-first. The kept set is
// the union of four layers: the most recent N, plus the newest snapshot in each
// of the most recent monthly / semiannual / yearly buckets.
func Select(times []time.Time, p Policy) []time.Time {
	sorted := append([]time.Time(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].After(sorted[j]) })

	keep := map[int64]bool{}
	add := func(t time.Time) { keep[t.UnixNano()] = true }

	for i := 0; i < p.Recent && i < len(sorted); i++ {
		add(sorted[i])
	}

	pickBuckets := func(keyOf func(time.Time) int, n int) {
		if n <= 0 {
			return
		}
		seen := map[int]bool{}
		var order []int
		newest := map[int]time.Time{}
		for _, t := range sorted { // desc order: first seen per bucket is newest
			k := keyOf(t)
			if !seen[k] {
				seen[k] = true
				order = append(order, k)
				newest[k] = t
			}
		}
		for i := 0; i < n && i < len(order); i++ {
			add(newest[order[i]])
		}
	}

	pickBuckets(func(t time.Time) int { return t.Year()*12 + int(t.Month()) - 1 }, p.Monthly)
	pickBuckets(func(t time.Time) int {
		h := 0
		if t.Month() > 6 {
			h = 1
		}
		return t.Year()*2 + h
	}, p.Semiannual)
	pickBuckets(func(t time.Time) int { return t.Year() }, p.Yearly)

	// Safety floor: always keep the most recent snapshot, regardless of policy.
	// This guarantees a sync never prunes the snapshot it just created — most
	// importantly under an all-zero policy, which would otherwise delete every
	// snapshot including the fresh one and wipe all history.
	if len(sorted) > 0 {
		add(sorted[0])
	}

	var out []time.Time
	for _, t := range sorted {
		if keep[t.UnixNano()] {
			out = append(out, t)
		}
	}
	return out
}

// Partition splits times into keep and delete sets (both newest-first).
func Partition(times []time.Time, p Policy) (keep, del []time.Time) {
	keep = Select(times, p)
	keepSet := map[int64]bool{}
	for _, t := range keep {
		keepSet[t.UnixNano()] = true
	}
	sorted := append([]time.Time(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].After(sorted[j]) })
	for _, t := range sorted {
		if !keepSet[t.UnixNano()] {
			del = append(del, t)
		}
	}
	return keep, del
}
