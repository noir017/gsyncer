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

func TestRealRunStreamEmitsLinesAndCapturesAll(t *testing.T) {
	var r Real
	var got []string
	res, err := r.RunStream(context.Background(), func(l string) { got = append(got, l) },
		"printf", `a\nb\nc\n`)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("onLine got %v, want %v", got, want)
	}
	// Full stdout is preserved (newline-joined) for downstream parsing.
	if res.Stdout != "a\nb\nc\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "a\nb\nc\n")
	}
}

func TestRealRunStreamSplitsOnCarriageReturn(t *testing.T) {
	var r Real
	var got []string
	// \r-terminated updates (as rsync --info=progress2 emits) must surface as
	// discrete lines, not one buffered blob.
	if _, err := r.RunStream(context.Background(), func(l string) { got = append(got, l) },
		"printf", `x\ry\rz\n`); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Join(got, ",") != "x,y,z" {
		t.Fatalf("onLine got %v, want [x y z]", got)
	}
}

func TestRealRunStreamNonZeroExit(t *testing.T) {
	var r Real
	res, err := r.RunStream(context.Background(), nil, "sh", "-c", "echo out; exit 3")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if res.Code != 3 {
		t.Fatalf("code = %d, want 3", res.Code)
	}
	if res.Stdout != "out\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "out\n")
	}
}

func TestScanLinesCRCoalescesCRLF(t *testing.T) {
	// A \r\n pair must be a single break (no spurious empty token).
	adv, tok, _ := scanLinesCR([]byte("hi\r\nrest"), false)
	if string(tok) != "hi" || adv != 4 {
		t.Fatalf("tok=%q adv=%d, want tok=hi adv=4", tok, adv)
	}
}

func TestRealRunStreamNilOnLine(t *testing.T) {
	// A nil onLine must not panic and must still capture stdout.
	var r Real
	res, err := r.RunStream(context.Background(), nil, "echo", "hello")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Stdout != "hello\n" {
		t.Fatalf("stdout = %q", res.Stdout)
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
	if f.Calls[0].Env != nil {
		t.Fatalf("Run should record nil env, got %v", f.Calls[0].Env)
	}
}

func TestRealRunEnvInjectsVars(t *testing.T) {
	var r Real
	res, err := r.RunEnv(context.Background(), []string{"GSYNC_TEST=hi"}, "sh", "-c", "echo $GSYNC_TEST")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hi" {
		t.Fatalf("GSYNC_TEST = %q, want hi", strings.TrimSpace(res.Stdout))
	}
}

func TestFakeRunnerRecordsEnv(t *testing.T) {
	f := &FakeRunner{}
	_, _ = f.RunEnv(context.Background(), []string{"GSYNC_NAME=web"}, "sh", "-c", "true")
	if len(f.Calls) != 1 || len(f.Calls[0].Env) != 1 || f.Calls[0].Env[0] != "GSYNC_NAME=web" {
		t.Fatalf("env not recorded: %+v", f.Calls)
	}
}
