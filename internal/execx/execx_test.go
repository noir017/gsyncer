package execx

import (
	"context"
	"strings"
	"testing"
)

func TestRealRunCapturesStdout(t *testing.T) {
	var r Real
	res, err := r.Run(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Stdout != "hello\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "hello\n")
	}
}

func TestRealRunForcesCLocale(t *testing.T) {
	var r Real
	// The child sees LC_ALL=C even if the test process has a different locale.
	res, err := r.Run(context.Background(), "sh", "-c", "echo $LC_ALL")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "C" {
		t.Fatalf("LC_ALL = %q, want C", strings.TrimSpace(res.Stdout))
	}
}

func TestRealRunNonZeroExit(t *testing.T) {
	var r Real
	res, err := r.Run(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if res.Code != 1 {
		t.Fatalf("code = %d, want 1", res.Code)
	}
}

func TestFakeRunnerRecordsAndResponds(t *testing.T) {
	f := &FakeRunner{Handler: func(name string, args []string) (Result, error) {
		return Result{Stdout: "ok"}, nil
	}}
	res, _ := f.Run(context.Background(), "rsync", "--version")
	if res.Stdout != "ok" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "rsync" || f.Calls[0].Args[0] != "--version" {
		t.Fatalf("calls not recorded: %+v", f.Calls)
	}
}
