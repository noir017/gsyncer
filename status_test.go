package main

import (
	"testing"
	"time"
)

func TestComputeStatusNoSnapshots(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	st := computeStatus("web", "hardlink", nil, now, 26)
	if st.Count != 0 || st.Latest != "" || !st.Stale {
		t.Fatalf("no-snapshot status = %+v", st)
	}
}

func TestComputeStatusFreshNotStale(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	times := []time.Time{now.Add(-2 * time.Hour)}
	st := computeStatus("web", "btrfs", times, now, 26)
	if st.Stale {
		t.Fatalf("2h-old should not be stale: %+v", st)
	}
	if st.Count != 1 || st.AgeHours < 1.9 || st.AgeHours > 2.1 {
		t.Fatalf("age wrong: %+v", st)
	}
}

func TestComputeStatusOldIsStale(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	times := []time.Time{now.Add(-48 * time.Hour)}
	st := computeStatus("web", "hardlink", times, now, 26)
	if !st.Stale {
		t.Fatalf("48h-old should be stale: %+v", st)
	}
}

func TestComputeStatusThresholdZeroNeverStale(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if computeStatus("a", "hardlink", nil, now, 0).Stale {
		t.Fatal("threshold 0 must not flag missing snapshots stale")
	}
	old := []time.Time{now.Add(-1000 * time.Hour)}
	if computeStatus("b", "hardlink", old, now, 0).Stale {
		t.Fatal("threshold 0 must not flag old snapshots stale")
	}
}

func TestComputeStatusPicksNewest(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	times := []time.Time{
		time.Date(2026, 6, 30, 3, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC), // newest
		time.Date(2026, 6, 29, 3, 0, 0, 0, time.UTC),
	}
	st := computeStatus("web", "hardlink", times, now, 26)
	if st.Latest != "2026-07-01_030000" {
		t.Fatalf("latest = %q", st.Latest)
	}
}
