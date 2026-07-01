// Package execx provides an injectable command runner so that callers can be
// tested without spawning real processes.
package execx

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
)

// Result holds the captured output of a command.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Runner executes an external command. RunEnv is like Run but adds extra
// environment variables (KEY=value) for the child process; it exists so hooks
// and notification commands can receive per-entry metadata without the caller
// interpolating it into a shell string.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Result, error)
	RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error)
}

// StreamRunner is an optional Runner extension that streams a command's stdout
// line by line via onLine while still returning the complete captured Result
// (so callers that scrape the full output, e.g. rsync --stats parsing, keep
// working). Callers type-assert for it and fall back to Run when it is absent,
// so test doubles need not implement it.
type StreamRunner interface {
	RunStream(ctx context.Context, onLine func(string), name string, args ...string) (Result, error)
}

// Real runs commands via os/exec.
type Real struct{}

// Run implements Runner.
func (r Real) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return r.RunEnv(ctx, nil, name, args...)
}

// RunEnv implements Runner.
func (Real) RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Force the C locale so tool output (notably rsync --stats labels and
	// number formatting) is stable and parseable regardless of the host locale.
	// Caller-supplied env comes last so a hook may still override it if needed.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	res := Result{Stdout: so.String(), Stderr: se.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.Code = ee.ExitCode()
	}
	return res, err
}

// RunStream implements StreamRunner. It captures stdout into the returned
// Result exactly like Run, but also invokes onLine for each output line as it
// arrives. Lines are split on both '\n' and '\r' so tools that redraw a line
// with a carriage return (rsync --info=progress2) surface as discrete updates.
func (Real) RunStream(ctx context.Context, onLine func(string), name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Match Run: force the C locale so output labels/number formatting are stable.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	var se bytes.Buffer
	cmd.Stderr = &se
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}

	var so bytes.Buffer
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	sc.Split(scanLinesCR)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// Keep the full stdout intact (newline-joined) for downstream parsing.
		so.WriteString(line)
		so.WriteByte('\n')
		if onLine != nil {
			onLine(line)
		}
	}

	err = cmd.Wait()
	res := Result{Stdout: so.String(), Stderr: se.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.Code = ee.ExitCode()
	}
	return res, err
}

// scanLinesCR is a bufio.SplitFunc that breaks on '\n' or '\r', coalescing a
// "\r\n" pair into a single break so no spurious empty token is produced.
func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		adv := i + 1
		if data[i] == '\r' && adv < len(data) && data[adv] == '\n' {
			adv++
		}
		return adv, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil // request more data
}

// Call records one invocation made against a FakeRunner.
type Call struct {
	Name string
	Args []string
	Env  []string // extra env passed via RunEnv (nil for Run)
}

// FakeRunner is a test double that records calls and returns scripted results.
type FakeRunner struct {
	Calls   []Call
	Handler func(name string, args []string) (Result, error)
}

// Run implements Runner.
func (f *FakeRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return f.RunEnv(ctx, nil, name, args...)
}

// RunEnv implements Runner.
func (f *FakeRunner) RunEnv(_ context.Context, env []string, name string, args ...string) (Result, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: args, Env: env})
	if f.Handler != nil {
		return f.Handler(name, args)
	}
	return Result{}, nil
}
