// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadString(t *testing.T, content string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

const entryTOML = `
[[sync]]
  name = "web"
  host = "h"
  user = "u"
  remote_path = "/r"
  local_path = "/data/web"
`

func TestScheduleEffectiveResolution(t *testing.T) {
	cfg, err := loadString(t, `
[defaults]
  schedule = "5 3 * * 2"
  on_offline = "skip"
  offline_retry_interval = "2h"
  offline_retry_limit = 3
`+entryTOML+`
[[sync]]
  name = "laptop"
  host = "h"
  user = "u"
  remote_path = "/r"
  local_path = "/data/laptop"
  schedule = "0 16 * * 1"
  on_offline = "retry"
  offline_retry_interval = "24h"
  offline_retry_limit = 0

[[sync]]
  name = "adhoc"
  host = "h"
  user = "u"
  remote_path = "/r"
  local_path = "/data/adhoc"
  schedule = "manual"
`)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Defaults
	web, laptop, adhoc := cfg.Sync[0], cfg.Sync[1], cfg.Sync[2]

	if got := web.EffectiveSchedule(d); got != "5 3 * * 2" {
		t.Errorf("web schedule = %q, want inherited default", got)
	}
	if web.EffectiveOnOffline(d) != OfflineSkip || web.EffectiveOfflineRetryInterval(d) != 2*time.Hour || web.EffectiveOfflineRetryLimit(d) != 3 {
		t.Errorf("web should inherit every offline default")
	}
	if got := laptop.EffectiveSchedule(d); got != "0 16 * * 1" {
		t.Errorf("laptop schedule = %q", got)
	}
	if laptop.EffectiveOnOffline(d) != OfflineRetry || laptop.EffectiveOfflineRetryInterval(d) != 24*time.Hour {
		t.Errorf("laptop overrides not applied")
	}
	if laptop.EffectiveOfflineRetryLimit(d) != 0 {
		t.Errorf("an explicit 0 must override the default limit back to unlimited")
	}
	if got := adhoc.EffectiveSchedule(d); got != "" {
		t.Errorf("schedule = \"manual\" must opt out of the default, got %q", got)
	}
}

func TestScheduleZeroValueDefaults(t *testing.T) {
	cfg, err := loadString(t, entryTOML)
	if err != nil {
		t.Fatal(err)
	}
	s, d := cfg.Sync[0], cfg.Defaults
	if s.EffectiveSchedule(d) != "" {
		t.Error("no schedule configured anywhere: manual-only")
	}
	if s.EffectiveOnOffline(d) != OfflineForce {
		t.Error("existing configs must keep today's behaviour (force)")
	}
	if s.EffectiveOfflineRetryInterval(d) != DefaultOfflineRetryInterval || s.EffectiveOfflineRetryLimit(d) != 0 {
		t.Error("retry defaults")
	}
}

func TestScheduleValidation(t *testing.T) {
	cases := []struct {
		name, toml, want string
	}{
		{"bad cron in defaults", "[defaults]\n  schedule = \"0 16 * *\"\n" + entryTOML, "defaults"},
		{"bad cron in entry", entryTOML + "  schedule = \"every monday\"\n", `sync "web"`},
		{"never fires", entryTOML + "  schedule = \"0 0 30 2 *\"\n", "never fires"},
		{"bad on_offline", entryTOML + "  on_offline = \"wait\"\n", "on_offline"},
		{"bad on_offline in defaults", "[defaults]\n  on_offline = \"later\"\n" + entryTOML, "on_offline"},
		{"unparseable interval", entryTOML + "  offline_retry_interval = \"1 day\"\n", "not a duration"},
		{"interval below tick", entryTOML + "  offline_retry_interval = \"30s\"\n", "at least"},
		{"negative limit", entryTOML + "  offline_retry_limit = -1\n", "offline_retry_limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadString(t, c.toml)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		24 * time.Hour:   "24h",
		30 * time.Minute: "30m",
		90 * time.Minute: "1h30m",
		90 * time.Second: "1m30s",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// Save round-trips the new fields, so a TUI save or any other rewrite of the
// config never drops an entry's schedule.
func TestScheduleFieldsSurviveSave(t *testing.T) {
	limit := 2
	c := &Config{
		Defaults: Defaults{Schedule: "5 3 * * 2", OnOffline: OfflineRetry},
		Sync: []Sync{{
			Name: "laptop", Host: "h", User: "u", RemotePath: "/r", LocalPath: "/data/l",
			Schedule: "0 16 * * 1", OnOffline: OfflineSkip, OfflineRetryInterval: "24h", OfflineRetryLimit: &limit,
		}},
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := got.Sync[0]
	if got.Defaults.Schedule != "5 3 * * 2" || got.Defaults.OnOffline != OfflineRetry {
		t.Errorf("defaults lost: %+v", got.Defaults)
	}
	if s.Schedule != "0 16 * * 1" || s.OnOffline != OfflineSkip || s.OfflineRetryInterval != "24h" ||
		s.OfflineRetryLimit == nil || *s.OfflineRetryLimit != 2 {
		t.Errorf("entry fields lost: %+v", s)
	}
}
