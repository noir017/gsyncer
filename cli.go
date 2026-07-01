package main

import (
	"fmt"
	"path/filepath"
	"time"

	"gsyncer/internal/config"
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
