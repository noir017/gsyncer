// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/syncer"
)

func TestTickSummaryLineExplainsOfflineOutcomes(t *testing.T) {
	next := time.Date(2026, 9, 29, 16, 0, 0, 0, time.Local)
	rep := []syncer.TickResult{
		{Result: syncer.Result{Name: "oracle-apps", OK: true}, Event: syncer.TickRan},
		{Result: syncer.Result{Name: "pacvue-record", Skipped: true}, Event: syncer.TickOfflineRetry, Attempts: 1, NextRun: next},
		{Result: syncer.Result{Name: "laptop", Err: errors.New("x")}, Event: syncer.TickOfflineGaveUp, Attempts: 7, NextRun: next},
	}
	got := tickSummaryLine(rep, 2*time.Second)
	for _, want := range []string{
		"成功 1 / 失败 1 / 跳过 1", "（排程）",
		"pacvue-record 不在线，2026-09-29 16:00 重试",
		"laptop 不在线，已放弃（共尝试 7 次",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func countLogs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(exeDir(), "logs"))
	if err != nil {
		return 0
	}
	return len(ents)
}

// The overwhelmingly common tick: nothing is due. It must exit 0 and leave no
// trace in logs/, or a once-a-minute cron would bury the real run logs.
func TestCmdTickNothingDueIsSilent(t *testing.T) {
	local := filepath.Join(t.TempDir(), "web")
	path := writeCfg(t, `
[[sync]]
  name = "web"
  host = "h"
  user = "u"
  remote_path = "/r"
  local_path = "`+local+`"
  schedule = "0 16 * * 1"
`)
	before := countLogs(t)
	if rc := cmdTick([]string{"--config", path}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if after := countLogs(t); after != before {
		t.Fatalf("a no-op tick wrote %d log file(s)", after-before)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "state", "web.json")); err != nil {
		t.Fatalf("first tick should record the entry's next run: %v", err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatal("a tick with nothing due must not touch local_path")
	}
}

// A broken state dir fails every tick; the alert goes out once an hour, not
// once a minute, and comes back immediately after the problem clears.
func TestThrottleTickErrors(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state") // unique per test → unique marker
	errRes := syncer.TickResult{Result: syncer.Result{Name: "web", Err: errors.New("read-only")}, Event: syncer.TickError}
	ran := syncer.TickResult{Result: syncer.Result{Name: "db", OK: true}, Event: syncer.TickRan}
	t0 := time.Now()

	kept, sup := throttleTickErrors([]syncer.TickResult{errRes, ran}, stateDir, t0)
	if len(kept) != 2 || len(sup) != 0 {
		t.Fatalf("first error must be reported: kept=%d suppressed=%d", len(kept), len(sup))
	}
	kept, sup = throttleTickErrors([]syncer.TickResult{errRes, ran}, stateDir, t0.Add(time.Minute))
	if len(kept) != 1 || kept[0].Name != "db" || len(sup) != 1 {
		t.Fatalf("a minute later the error is held back, the run is not: kept=%v suppressed=%d", kept, len(sup))
	}
	if kept, _ := throttleTickErrors([]syncer.TickResult{errRes}, stateDir, t0.Add(61*time.Minute)); len(kept) != 1 {
		t.Fatal("after the interval the error is reported again")
	}
	// A clean tick clears the marker, so a new failure alerts at once.
	throttleTickErrors(nil, stateDir, t0.Add(62*time.Minute))
	if kept, _ := throttleTickErrors([]syncer.TickResult{errRes}, stateDir, t0.Add(63*time.Minute)); len(kept) != 1 {
		t.Fatal("after recovery the next error must be reported immediately")
	}
	throttleTickErrors(nil, stateDir, t0) // leave no marker behind in the shared temp dir
}

func TestNotifiableExcludesIntendedOfflineOutcomes(t *testing.T) {
	cases := map[syncer.TickEvent]bool{
		syncer.TickRan:           true,
		syncer.TickOfflineGaveUp: true,
		syncer.TickError:         true,
		syncer.TickOfflineRetry:  false,
		syncer.TickOfflineSkip:   false,
		syncer.TickWouldRun:      false,
	}
	for ev, want := range cases {
		if got := (syncer.TickResult{Event: ev, Attempts: 1}).Notifiable(); got != want {
			t.Errorf("event %d: Notifiable = %v, want %v", ev, got, want)
		}
	}
}

// Without any schedule the status table keeps its old columns, so scripts
// that parse it keep working.
func TestStatusTableUnchangedWithoutSchedules(t *testing.T) {
	var b bytes.Buffer
	writeStatusTable(&b, []EntryStatus{{Name: "web", Backend: "btrfs", Count: 3}})
	if head := strings.SplitN(b.String(), "\n", 2)[0]; strings.Contains(head, "NEXT") || !strings.HasSuffix(strings.TrimSpace(head), "STALE") {
		t.Fatalf("header changed for a manual-only config: %q", head)
	}
	b.Reset()
	writeStatusTable(&b, []EntryStatus{{Name: "web", Schedule: "5 3 * * 2", NextRun: "2026-09-29 03:05", OfflineAttempts: 2}})
	if out := b.String(); !strings.Contains(out, "NEXT") || !strings.Contains(out, "2026-09-29 03:05") || !strings.Contains(out, "已尝试 2 次") {
		t.Fatalf("scheduled status missing columns:\n%s", out)
	}
}

func TestSchedulePreviewForCheck(t *testing.T) {
	limit := 0
	entries := []config.Sync{
		{Name: "oracle-apps"},
		{Name: "pacvue-record", Schedule: "0 16 * * 1", OnOffline: config.OfflineRetry,
			OfflineRetryInterval: "24h", OfflineRetryLimit: &limit},
		{Name: "adhoc", Schedule: config.ScheduleManual},
	}
	d := config.Defaults{Schedule: "5 3 * * 2"}
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.Local) // Thursday
	var b bytes.Buffer
	writeSchedulePreview(&b, entries, d, now)
	out := b.String()
	for _, want := range []string{
		"oracle-apps", "5 3 * * 2", "下次 2026-09-29 03:05", "不探测，照常同步",
		"pacvue-record", "下次 2026-09-28 16:00", "离线则每 24h 重试（直到下一个排程点）",
		"仅手动（无 schedule）: adhoc",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview missing %q:\n%s", want, out)
		}
	}

	b.Reset()
	writeSchedulePreview(&b, []config.Sync{{Name: "x"}}, config.Defaults{}, now)
	if b.Len() != 0 {
		t.Fatalf("manual-only configs keep the old check output, got %q", b.String())
	}
}
