// Package execx provides an injectable command runner so that callers can be
// tested without spawning real processes.
package execx

import (
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

// Runner executes an external command.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Result, error)
}

// Real runs commands via os/exec.
type Real struct{}

// Run implements Runner.
func (Real) Run(ctx context.Context, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Force the C locale so tool output (notably rsync --stats labels and
	// number formatting) is stable and parseable regardless of the host locale.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
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

// Call records one invocation made against a FakeRunner.
type Call struct {
	Name string
	Args []string
}

// FakeRunner is a test double that records calls and returns scripted results.
type FakeRunner struct {
	Calls   []Call
	Handler func(name string, args []string) (Result, error)
}

// Run implements Runner.
func (f *FakeRunner) Run(_ context.Context, name string, args ...string) (Result, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: args})
	if f.Handler != nil {
		return f.Handler(name, args)
	}
	return Result{}, nil
}
