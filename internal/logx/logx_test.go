package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, _ := time.Parse("2006-01-02_150405", s)
	return t
}

func TestRunLoggerWritesFile(t *testing.T) {
	dir := t.TempDir()
	l, err := NewRunLogger(dir, ts("2026-06-24_030000"))
	if err != nil {
		t.Fatal(err)
	}
	l.Infof("hello %s", "world")
	l.Errorf("boom %d", 7)
	l.Close()
	data, err := os.ReadFile(filepath.Join(dir, "2026-06-24_030000.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "hello world") || !strings.Contains(s, "boom 7") {
		t.Fatalf("log content = %q", s)
	}
	if !strings.Contains(s, "INFO") || !strings.Contains(s, "ERROR") {
		t.Fatalf("missing levels: %q", s)
	}
}

func TestAppendSummary(t *testing.T) {
	dir := t.TempDir()
	if err := AppendSummary(dir, "run A ok"); err != nil {
		t.Fatal(err)
	}
	if err := AppendSummary(dir, "run B ok"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "summary.log"))
	if c := strings.Count(string(data), "\n"); c != 2 {
		t.Fatalf("want 2 lines, content=%q", data)
	}
}

func TestCleanupByCountAndDays(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"2026-06-24_030000.log",
		"2026-06-23_030000.log",
		"2026-01-01_030000.log", // old
		"summary.log",           // must survive
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := ts("2026-06-24_040000")
	// keepDays=30 removes the Jan file; keepCount=10 keeps the rest.
	if err := Cleanup(dir, 30, 10, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-01-01_030000.log")); err == nil {
		t.Fatal("old log should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.log")); err != nil {
		t.Fatal("summary.log must survive")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-06-24_030000.log")); err != nil {
		t.Fatal("recent log must survive")
	}
}

func TestCleanupByCountOnly(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"2026-06-24_030000.log", // newest
		"2026-06-23_030000.log",
		"2026-06-22_030000.log", // oldest -> should be pruned
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// keepDays=0 disables the age rule; keepCount=2 keeps the 2 newest.
	if err := Cleanup(dir, 0, 2, ts("2026-06-24_040000")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-06-22_030000.log")); err == nil {
		t.Fatal("oldest log should be pruned by count")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-06-24_030000.log")); err != nil {
		t.Fatal("newest log must survive")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-06-23_030000.log")); err != nil {
		t.Fatal("second-newest log must survive")
	}
}
