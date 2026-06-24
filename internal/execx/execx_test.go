package execx

import (
	"context"
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
