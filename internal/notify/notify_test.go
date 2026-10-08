// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/syncer"
)

func sampleResults() ([]syncer.Result, []config.Sync) {
	results := []syncer.Result{
		{Name: "web", OK: true, Files: 5, Bytes: 42, Duration: 2 * time.Second},
		{Name: "db", OK: false, Err: errString("rsync failed")},
	}
	entries := []config.Sync{
		{Name: "web", Host: "h1"},
		{Name: "db", Host: "h2"},
	}
	return results, entries
}

type errString string

func (e errString) Error() string { return string(e) }

func TestBuildStatusFailureWhenAnyFailed(t *testing.T) {
	results, entries := sampleResults()
	p := Build(results, entries, 3*time.Second)
	if p.Status != "failure" {
		t.Fatalf("status = %q, want failure", p.Status)
	}
	if p.OK != 1 || p.Failed != 1 {
		t.Fatalf("counts = %+v", p)
	}
	if p.Entries[0].Host != "h1" || p.Entries[1].Host != "h2" {
		t.Fatalf("hosts not joined: %+v", p.Entries)
	}
	if p.Entries[1].Error != "rsync failed" {
		t.Fatalf("error not captured: %+v", p.Entries[1])
	}
}

func TestBuildSkippedOnlyIsSuccess(t *testing.T) {
	results := []syncer.Result{{Name: "web", Skipped: true}}
	p := Build(results, []config.Sync{{Name: "web"}}, time.Second)
	if p.Status != "success" || p.Skipped != 1 {
		t.Fatalf("skipped-only should be success: %+v", p)
	}
}

func TestShouldSendGating(t *testing.T) {
	fail := Payload{Status: "failure"}
	ok := Payload{Status: "success"}
	if ShouldSend(config.NotifyConfig{OnFailure: true}, fail) != true {
		t.Fatal("failure+on_failure should send")
	}
	if ShouldSend(config.NotifyConfig{OnFailure: true}, ok) != false {
		t.Fatal("success with only on_failure should not send")
	}
	if ShouldSend(config.NotifyConfig{OnSuccess: true}, ok) != true {
		t.Fatal("success+on_success should send")
	}
	if ShouldSend(config.NotifyConfig{}, fail) != false {
		t.Fatal("no switches should never send")
	}
}

func TestSendWebhookPostsJSON(t *testing.T) {
	var gotBody []byte
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	results, entries := sampleResults()
	p := Build(results, entries, 3*time.Second)
	cfg := config.NotifyConfig{OnFailure: true, Webhook: srv.URL}
	if err := Send(context.Background(), cfg, p, srv.Client(), &execx.FakeRunner{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotType != "application/json" {
		t.Fatalf("content-type = %q", gotType)
	}
	var decoded Payload
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if decoded.Status != "failure" || decoded.Failed != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestSendWebhookErrorsOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := Payload{Status: "failure", Failed: 1}
	cfg := config.NotifyConfig{OnFailure: true, Webhook: srv.URL}
	if err := Send(context.Background(), cfg, p, srv.Client(), &execx.FakeRunner{}); err == nil {
		t.Fatal("expected error on 500 status")
	}
}

func TestSendCommandRunsWithEnv(t *testing.T) {
	fr := &execx.FakeRunner{}
	p := Payload{Status: "failure", OK: 1, Failed: 2, Skipped: 0, DurationSec: 3.4}
	cfg := config.NotifyConfig{OnFailure: true, Command: "mail admin"}
	if err := Send(context.Background(), cfg, p, nil, fr); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(fr.Calls) != 1 {
		t.Fatalf("want 1 command call, got %d", len(fr.Calls))
	}
	c := fr.Calls[0]
	if c.Name != "sh" || c.Args[0] != "-c" || c.Args[1] != "mail admin" {
		t.Fatalf("bad command invocation: %+v", c)
	}
	env := strings.Join(c.Env, "\n")
	for _, want := range []string{"GSYNC_STATUS=failure", "GSYNC_FAILED=2", "GSYNC_TEXT=备份失败", "GSYNC_JSON="} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q in %v", want, c.Env)
		}
	}
}

// Text leads with the headline and lists failures before successes, so the
// entry that needs attention is the first thing read in a long batch.
func TestTextListsFailuresFirst(t *testing.T) {
	results, entries := sampleResults()
	got := Text(Build(results, entries, 3*time.Second))
	want := "备份失败：成功 1 / 失败 1 / 耗时 3s\n" +
		"✗ db（h2）rsync failed\n" +
		"✓ web（h1）传输 5 个文件 / 42 B / 2s"
	if got != want {
		t.Fatalf("Text =\n%s\nwant\n%s", got, want)
	}
}

func TestTextSuccessAndDryRun(t *testing.T) {
	p := Build([]syncer.Result{
		{Name: "web", OK: true, Files: 3, Bytes: 5 << 20, Duration: 75 * time.Second},
		{Name: "db", Skipped: true, Err: errString("locked")},
	}, []config.Sync{{Name: "web", Host: "h1"}, {Name: "db", Host: "h2"}}, 90*time.Second)
	p.DryRun = true
	got := Text(p)
	want := "备份成功（预演）：成功 1 / 失败 0 / 跳过 1 / 耗时 1m30s\n" +
		"- db（h2）跳过：locked\n" +
		"✓ web（h1）传输 3 个文件 / 5.0 MB / 1m15s"
	if got != want {
		t.Fatalf("Text =\n%s\nwant\n%s", got, want)
	}
}

// A multi-line stderr tail must fold to one capped line: the message is for a
// phone screen, and the full error is in the run log.
func TestTextFoldsAndCapsErrors(t *testing.T) {
	long := "ssh: connect\n  to host x\n" + strings.Repeat("é", maxErrRunes)
	p := Build([]syncer.Result{{Name: "db", Err: errString(long)}},
		[]config.Sync{{Name: "db", Host: "h2"}}, time.Second)
	lines := strings.Split(Text(p), "\n")
	if len(lines) != 2 {
		t.Fatalf("error not folded onto one line: %q", lines)
	}
	if !strings.HasPrefix(lines[1], "✗ db（h2）ssh: connect to host x é") || !strings.HasSuffix(lines[1], "é…") {
		t.Fatalf("bad error line: %q", lines[1])
	}
	if n := len([]rune(strings.TrimPrefix(lines[1], "✗ db（h2）"))); n != maxErrRunes+1 {
		t.Fatalf("error is %d runes, want %d (cap + ellipsis)", n, maxErrRunes+1)
	}
}

// A failing webhook must not prevent the command sink from running, and both
// errors should be reported (redundant channels are independent).
func TestSendAttemptsBothSinksIndependently(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // webhook fails
	}))
	defer srv.Close()
	fr := &execx.FakeRunner{}
	cfg := config.NotifyConfig{OnFailure: true, Webhook: srv.URL, Command: "mail admin"}
	err := Send(context.Background(), cfg, Payload{Status: "failure", Failed: 1}, srv.Client(), fr)
	if err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("expected webhook error reported, got %v", err)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Name != "sh" {
		t.Fatalf("command sink must still run despite webhook failure: %+v", fr.Calls)
	}
}

func TestSendSkipsWhenGatedOff(t *testing.T) {
	fr := &execx.FakeRunner{}
	p := Payload{Status: "success"}
	cfg := config.NotifyConfig{OnFailure: true, Command: "mail admin"} // success, on_failure only
	if err := Send(context.Background(), cfg, p, nil, fr); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(fr.Calls) != 0 {
		t.Fatalf("gated-off run must not invoke command: %+v", fr.Calls)
	}
}
