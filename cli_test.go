package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/syncer"
)

func TestResolveConfigPath(t *testing.T) {
	if got := resolveConfigPath("", "/opt/app"); got != "/opt/app/config.toml" {
		t.Fatalf("got %q", got)
	}
	if got := resolveConfigPath("/custom.toml", "/opt/app"); got != "/custom.toml" {
		t.Fatalf("got %q", got)
	}
}

func TestSelectEntries(t *testing.T) {
	all := []config.Sync{
		{Name: "a", Host: "h1"},
		{Name: "b", Host: "h2"},
		{Name: "c", Host: "h1"},
	}
	if got := selectEntries(all, "", ""); len(got) != 3 {
		t.Fatalf("all: %d", len(got))
	}
	if got := selectEntries(all, "b", ""); len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("by name: %v", got)
	}
	if got := selectEntries(all, "", "h1"); len(got) != 2 {
		t.Fatalf("by server: %v", got)
	}
}

func TestSummaryLine(t *testing.T) {
	res := []syncer.Result{
		{Name: "a", OK: true},
		{Name: "b", OK: false, Err: errors.New("x")},
	}
	got := summaryLine(res, 3*time.Second)
	if !strings.Contains(got, "成功 1") || !strings.Contains(got, "失败 1") ||
		!strings.Contains(got, "3") {
		t.Fatalf("summary = %q", got)
	}
}
