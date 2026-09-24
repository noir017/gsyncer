// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package main

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/schedule"
	"gsyncer/internal/syncer"
)

// resolveConfigPath returns the explicit flag value or exeDir/config.toml.
func resolveConfigPath(flag, exeDir string) string {
	if flag != "" {
		return flag
	}
	return filepath.Join(exeDir, "config.toml")
}

// knownHostsPath returns the per-config ssh known_hosts store, kept alongside
// the config file so it persists across cron runs.
func knownHostsPath(cfgFlag, exeDir string) string {
	return filepath.Join(filepath.Dir(resolveConfigPath(cfgFlag, exeDir)), "known_hosts")
}

// statePath returns the directory holding per-entry schedule state. Like
// known_hosts it sits beside the config (typically on the fast disk), not
// under any entry's local_path.
func statePath(cfgFlag, exeDir string) string {
	return filepath.Join(filepath.Dir(resolveConfigPath(cfgFlag, exeDir)), "state")
}

// selectEntries filters by entry name and/or server host. Empty filters match all.
func selectEntries(all []config.Sync, name, server string) []config.Sync {
	var out []config.Sync
	for _, s := range all {
		if name != "" && s.Name != name {
			continue
		}
		if server != "" && s.Host != server {
			continue
		}
		out = append(out, s)
	}
	return out
}

// summaryLine formats the one-line run summary.
func summaryLine(results []syncer.Result, dur time.Duration) string {
	ok, fail, skip := 0, 0, 0
	for _, r := range results {
		switch {
		case r.OK:
			ok++
		case r.Skipped:
			skip++
		default:
			fail++
		}
	}
	if skip > 0 {
		return fmt.Sprintf("成功 %d / 失败 %d / 跳过 %d / 耗时 %.1fs", ok, fail, skip, dur.Seconds())
	}
	return fmt.Sprintf("成功 %d / 失败 %d / 耗时 %.1fs", ok, fail, dur.Seconds())
}

// tickSummaryLine is summaryLine for a scheduled run, tagged as such and
// followed by one note per entry that met an offline host, so summary.log says
// why a backup did not happen, not just that one was skipped.
func tickSummaryLine(rep []syncer.TickResult, dur time.Duration) string {
	plain := make([]syncer.Result, len(rep))
	for i, r := range rep {
		plain[i] = r.Result
	}
	var b strings.Builder
	b.WriteString(summaryLine(plain, dur))
	b.WriteString("（排程）")
	for _, r := range rep {
		switch r.Event {
		case syncer.TickOfflineRetry:
			fmt.Fprintf(&b, " · %s 不在线，%s 重试", r.Name, r.NextRun.Format(syncer.HumanTime))
		case syncer.TickOfflineSkip:
			fmt.Fprintf(&b, " · %s 不在线，已跳过本次（下次 %s）", r.Name, r.NextRun.Format(syncer.HumanTime))
		case syncer.TickOfflineGaveUp:
			fmt.Fprintf(&b, " · %s 不在线，已放弃（共尝试 %d 次，下次 %s）",
				r.Name, r.Attempts, r.NextRun.Format(syncer.HumanTime))
		case syncer.TickError:
			fmt.Fprintf(&b, " · %s 排程出错: %v", r.Name, r.Err)
		}
	}
	return b.String()
}

// errorAlertInterval: a broken state dir fails every tick — every minute from
// cron — so scheduling errors are reported at most this often.
const errorAlertInterval = time.Hour

// throttleTickErrors splits scheduling errors out of rep when one was already
// reported for this state dir within errorAlertInterval, returning what should
// still be reported and what was held back.
//
// The marker lives in the system temp dir, not beside the state: the state dir
// being unwritable is precisely the failure being throttled. A tick without
// scheduling errors removes it, so the next new failure is reported at once.
func throttleTickErrors(rep []syncer.TickResult, stateDir string, now time.Time) (kept, suppressed []syncer.TickResult) {
	h := fnv.New32a()
	h.Write([]byte(stateDir))
	marker := filepath.Join(os.TempDir(), fmt.Sprintf("gsyncer-tick-error-%08x", h.Sum32()))

	hasErr := false
	for _, r := range rep {
		if r.Event == syncer.TickError {
			hasErr = true
			break
		}
	}
	if !hasErr {
		_ = os.Remove(marker)
		return rep, nil
	}
	if info, err := os.Stat(marker); err == nil && now.Sub(info.ModTime()) < errorAlertInterval {
		for _, r := range rep {
			if r.Event == syncer.TickError {
				suppressed = append(suppressed, r)
			} else {
				kept = append(kept, r)
			}
		}
		return kept, suppressed
	}
	if err := os.WriteFile(marker, nil, 0o600); err == nil {
		_ = os.Chtimes(marker, now, now)
	}
	return rep, nil
}

// countScheduled returns how many entries tick would ever consider.
func countScheduled(entries []config.Sync, d config.Defaults) int {
	n := 0
	for _, s := range entries {
		if s.EffectiveSchedule(d) != "" {
			n++
		}
	}
	return n
}

// offlinePolicyText describes an entry's offline handling for humans.
func offlinePolicyText(s config.Sync, d config.Defaults) string {
	switch s.EffectiveOnOffline(d) {
	case config.OfflineRetry:
		limit := "直到下一个排程点"
		if n := s.EffectiveOfflineRetryLimit(d); n > 0 {
			limit = fmt.Sprintf("最多 %d 次", n)
		}
		return fmt.Sprintf("离线则每 %s 重试（%s）", config.FormatDuration(s.EffectiveOfflineRetryInterval(d)), limit)
	case config.OfflineSkip:
		return "离线则跳过本次"
	default:
		return "不探测，照常同步"
	}
}

// writeSchedulePreview lists each scheduled entry's next run, for `check`. It
// prints nothing for a config with no schedules at all, keeping `check` output
// unchanged for manual-only setups.
func writeSchedulePreview(w io.Writer, entries []config.Sync, d config.Defaults, now time.Time) {
	if countScheduled(entries, d) == 0 {
		return
	}
	var manual []string
	for _, s := range entries {
		expr := s.EffectiveSchedule(d)
		if expr == "" {
			manual = append(manual, s.Name)
			continue
		}
		spec, err := schedule.Parse(expr)
		if err != nil {
			continue // Validate already rejected it
		}
		fmt.Fprintf(w, "  %-28s %-14s 下次 %s  %s\n", s.Name, expr,
			spec.Next(now).Format(syncer.HumanTime), offlinePolicyText(s, d))
	}
	if len(manual) > 0 {
		fmt.Fprintf(w, "  仅手动（无 schedule）: %s\n", strings.Join(manual, ", "))
	}
}

// writeTickPlan prints what a dry-run tick found for every entry.
func writeTickPlan(w io.Writer, entries []config.Sync, d config.Defaults, results []syncer.TickResult, now time.Time) {
	for i, r := range results {
		s := entries[i]
		expr := s.EffectiveSchedule(d)
		next := "-"
		if !r.NextRun.IsZero() {
			next = r.NextRun.Format(syncer.HumanTime)
		}
		var what string
		switch r.Event {
		case syncer.TickIdle:
			switch {
			case expr == "":
				expr, what = "-", "仅手动"
			case !r.NextRun.After(now):
				what = "已到点，但该条目正被另一个同步占用"
			default:
				what = "未到点"
			}
		case syncer.TickWouldRun:
			what = "到点：将开始同步"
		case syncer.TickOfflineRetry:
			what = fmt.Sprintf("到点：不在线，将于 %s 重试（第 %d 次尝试）", next, r.Attempts)
		case syncer.TickOfflineSkip:
			what = "到点：不在线，将跳过本次"
		case syncer.TickOfflineGaveUp:
			what = fmt.Sprintf("到点：不在线，将放弃本次（第 %d 次尝试）", r.Attempts)
		case syncer.TickError:
			what = fmt.Sprintf("错误: %v", r.Err)
		}
		fmt.Fprintf(w, "%-28s %-14s %-16s  %s\n", r.Name, expr, next, what)
		if r.Detail != "" {
			fmt.Fprintf(w, "%-28s   探测: %s\n", "", firstLine(r.Detail))
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
