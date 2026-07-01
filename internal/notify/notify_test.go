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
	for _, want := range []string{"GSYNC_STATUS=failure", "GSYNC_FAILED=2", "GSYNC_JSON="} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q in %v", want, c.Env)
		}
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
