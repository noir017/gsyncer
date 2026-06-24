package retention

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02_150405", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRecentOnly(t *testing.T) {
	in := []time.Time{
		d("2026-06-01_000000"), d("2026-06-02_000000"), d("2026-06-03_000000"),
	}
	keep := Select(in, Policy{Recent: 2})
	if len(keep) != 2 || !keep[0].Equal(d("2026-06-03_000000")) || !keep[1].Equal(d("2026-06-02_000000")) {
		t.Fatalf("keep = %v", keep)
	}
}

func TestMonthlyCollapsesSameMonth(t *testing.T) {
	in := []time.Time{
		d("2026-06-01_000000"), d("2026-06-15_000000"), d("2026-05-10_000000"),
	}
	// monthly=2: keep newest of June and newest of May.
	keep := Select(in, Policy{Monthly: 2})
	if len(keep) != 2 {
		t.Fatalf("want 2, got %d: %v", len(keep), keep)
	}
	if !keep[0].Equal(d("2026-06-15_000000")) || !keep[1].Equal(d("2026-05-10_000000")) {
		t.Fatalf("keep = %v", keep)
	}
}

func TestUnionAcrossLayers(t *testing.T) {
	in := []time.Time{
		d("2026-06-20_000000"), d("2026-06-19_000000"), // recent
		d("2026-01-05_000000"), // older, distinct month/half
		d("2024-03-03_000000"), // distinct year
	}
	keep := Select(in, Policy{Recent: 1, Monthly: 1, Yearly: 2})
	// recent -> 06-20; monthly newest month -> 06-20 (dup); yearly newest 2 years
	// -> 2026 newest (06-20) + 2024 newest (2024-03-03).
	got := map[string]bool{}
	for _, k := range keep {
		got[k.Format("2006-01-02")] = true
	}
	if !got["2026-06-20"] || !got["2024-03-03"] {
		t.Fatalf("missing expected keeps: %v", keep)
	}
	if got["2026-06-19"] || got["2026-01-05"] {
		t.Fatalf("kept something it should drop: %v", keep)
	}
}

func TestPartitionAndEmpty(t *testing.T) {
	if k := Select(nil, Policy{Recent: 5}); len(k) != 0 {
		t.Fatalf("empty input should keep nothing: %v", k)
	}
	in := []time.Time{d("2026-06-01_000000"), d("2026-06-02_000000")}
	keep, del := Partition(in, Policy{Recent: 1})
	if len(keep) != 1 || len(del) != 1 {
		t.Fatalf("keep=%v del=%v", keep, del)
	}
	if !del[0].Equal(d("2026-06-01_000000")) {
		t.Fatalf("del = %v", del)
	}
}

func TestZeroPolicyKeepsNothing(t *testing.T) {
	in := []time.Time{d("2026-06-01_000000")}
	if k := Select(in, Policy{}); len(k) != 0 {
		t.Fatalf("zero policy should keep nothing: %v", k)
	}
}
