// Package syncer orchestrates the per-entry pipeline: preflight, rsync pull,
// snapshot, and retention prune.
package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/retention"
	"gsync/internal/snapshot"
)

// progressInterval throttles how often streamed rsync progress lines are
// forwarded to the log, so a fast --info=progress2 stream cannot flood the
// UI channel or bloat the run log.
const progressInterval = 500 * time.Millisecond

// runRsync executes the rsync transfer. When the runner supports streaming
// (execx.Real) it forwards throttled progress lines to deps.Log so the run
// screen updates live; otherwise it falls back to a blocking capture, keeping
// test fakes and other Runner implementations working unchanged. Either way it
// returns the full captured Result so parseStats still sees the stats block.
func runRsync(ctx context.Context, deps Deps, name string, args []string) (execx.Result, error) {
	sr, ok := deps.Runner.(execx.StreamRunner)
	if !ok {
		return deps.Runner.Run(ctx, "rsync", args...)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	var last time.Time
	var started bool
	onLine := func(l string) {
		l = strings.TrimSpace(l)
		if l == "" {
			return
		}
		t := now()
		if started && t.Sub(last) < progressInterval {
			return
		}
		started, last = true, t
		deps.Log.Infof("[%s] %s", name, l)
	}
	return sr.RunStream(ctx, onLine, "rsync", args...)
}

// Logger is the subset of logging the syncer needs.
type Logger interface {
	Infof(format string, a ...any)
	Errorf(format string, a ...any)
}

// Deps are the injectable dependencies for syncing.
type Deps struct {
	Runner execx.Runner
	FSType snapshot.FSTypeFunc
	Log    Logger
	Now    func() time.Time
	// KnownHostsFile pins the ssh known_hosts store (per-config directory). Empty
	// falls back to ssh's default (~/.ssh/known_hosts).
	KnownHostsFile string
}

// Result is the outcome of syncing one entry.
type Result struct {
	Name     string
	OK       bool
	Skipped  bool // entry was not run (e.g. another sync holds the lock)
	Err      error
	Files    int64
	Bytes    int64
	Snapshot string
	Mode     string
	Pruned   int
	Duration time.Duration // wall-clock time spent on this entry
}

// rsyncPartialWarning reports whether an rsync exit code represents a
// partial-but-usable transfer that should not abort the pipeline:
//
//	23 = partial transfer due to error (some files could not be transferred)
//	24 = partial transfer due to vanished source files (routine on a live tree)
func rsyncPartialWarning(code int) bool {
	return code == 23 || code == 24
}

func toPolicy(r config.Retention) retention.Policy {
	return retention.Policy{
		Recent: r.Recent, Monthly: r.Monthly,
		Semiannual: r.Semiannual, Yearly: r.Yearly,
	}
}

// SyncOne runs the full pipeline for a single entry. It never panics; failures
// are reported via Result.Err with res.OK == false.
func SyncOne(ctx context.Context, s config.Sync, d config.Defaults, deps Deps, dryRun bool) (res Result) {
	res = Result{Name: s.Name}
	// Measure wall-clock per entry so notifications and status can report it.
	// deps.Now is the injected clock; a fixed test clock yields a zero duration.
	start := deps.Now()
	defer func() { res.Duration = deps.Now().Sub(start) }()
	port := s.EffectivePort(d)

	// Serialize runs sharing this local root so overlapping cron ticks don't
	// rsync --delete into the same current/ concurrently.
	lock, ok, err := acquireLock(s.LocalPath)
	if err != nil {
		deps.Log.Errorf("[%s] lock %s: %v", s.Name, s.LocalPath, err)
		res.Err = err
		return res
	}
	if !ok {
		deps.Log.Errorf("[%s] another sync is in progress for %s; skipping", s.Name, s.LocalPath)
		res.Skipped = true
		return res
	}
	defer lock.release()

	if _, err := deps.Runner.Run(ctx, "rsync", "--version"); err != nil {
		deps.Log.Errorf("[%s] local rsync missing: %s", s.Name, installHint())
		res.Err = err
		return res
	}
	if _, err := deps.Runner.Run(ctx, "ssh",
		sshCmdArgs(s.Identity, port, s.StrictHostKey, deps.KnownHostsFile, s.User, s.Host, "command -v rsync")...); err != nil {
		deps.Log.Errorf("[%s] remote rsync missing on %s: %s", s.Name, s.Host, installHint())
		res.Err = err
		return res
	}

	be := snapshot.Detect(ctx, s.LocalPath, deps.Runner, deps.FSType)
	cur, err := be.EnsureCurrent(ctx, s.LocalPath)
	if errors.Is(err, snapshot.ErrCurrentNotSubvolume) {
		deps.Log.Errorf("[%s] current is not a subvolume; falling back to hardlink", s.Name)
		be = snapshot.NewHardlink(deps.Runner)
		cur, err = be.EnsureCurrent(ctx, s.LocalPath)
	}
	if err != nil {
		deps.Log.Errorf("[%s] ensure current: %v", s.Name, err)
		res.Err = err
		return res
	}
	res.Mode = be.Name()
	deps.Log.Infof("[%s] snapshot mode: %s", s.Name, be.Name())

	out, err := runRsync(ctx, deps, s.Name, buildRsyncArgs(s, port, cur, dryRun, deps.KnownHostsFile, s.EffectiveBwlimit(d)))
	if err != nil {
		if rsyncPartialWarning(out.Code) {
			// 23/24 mean the transfer mostly succeeded (some files failed or
			// vanished mid-copy — routine on a live source). Treat as a warning
			// and still snapshot, rather than skipping the backup entirely.
			deps.Log.Errorf("[%s] rsync completed with warnings (exit %d), continuing: %s", s.Name, out.Code, out.Stderr)
		} else {
			deps.Log.Errorf("[%s] rsync failed: %v: %s", s.Name, err, out.Stderr)
			res.Err = err
			return res
		}
	}
	res.Files, res.Bytes = parseStats(out.Stdout)
	deps.Log.Infof("[%s] pulled %d files, %d bytes", s.Name, res.Files, res.Bytes)

	if dryRun {
		res.OK = true
		return res
	}

	ts := deps.Now()
	snap, err := be.Create(ctx, s.LocalPath, ts)
	if err != nil && be.Name() == "btrfs" {
		// btrfs snapshot can fail after EnsureCurrent passed (quota exhausted,
		// current swapped to a plain dir, etc.). Rather than abort with no
		// snapshot at all, fall back to a hardlink copy of current — it works on
		// any filesystem and preserves this run's backup.
		deps.Log.Errorf("[%s] btrfs snapshot failed (%v); retrying with hardlink", s.Name, err)
		be = snapshot.NewHardlink(deps.Runner)
		res.Mode = be.Name()
		deps.Log.Infof("[%s] snapshot mode: %s", s.Name, be.Name())
		snap, err = be.Create(ctx, s.LocalPath, ts)
	}
	if err != nil {
		deps.Log.Errorf("[%s] snapshot failed: %v", s.Name, err)
		res.Err = err
		return res
	}
	res.Snapshot = snap
	deps.Log.Infof("[%s] snapshot created: %s", s.Name, snap)

	times, err := be.List(s.LocalPath)
	if err != nil {
		deps.Log.Errorf("[%s] list snapshots: %v", s.Name, err)
		res.Err = err
		return res
	}
	_, del := retention.Partition(times, toPolicy(s.EffectiveRetention(d)))
	for _, t := range del {
		p := filepath.Join(s.LocalPath, "snapshots", t.Format(snapshot.TSLayout))
		if err := be.Delete(ctx, p); err != nil {
			deps.Log.Errorf("[%s] prune %s: %v", s.Name, p, err)
			continue
		}
		res.Pruned++
	}
	deps.Log.Infof("[%s] pruned %d snapshots", s.Name, res.Pruned)

	res.OK = true
	return res
}

// SyncMany runs entries sequentially, isolating per-entry failures.
func SyncMany(ctx context.Context, entries []config.Sync, d config.Defaults, deps Deps, dryRun bool) []Result {
	results := make([]Result, 0, len(entries))
	for _, s := range entries {
		if ctx.Err() != nil {
			// The run was cancelled (ctrl+c) or timed out. Stop launching further
			// entries instead of running them against a dead context, which would
			// fail preflight immediately and report spurious FAILED results.
			deps.Log.Errorf("run cancelled; %d entr(ies) skipped", len(entries)-len(results))
			break
		}
		results = append(results, SyncOne(ctx, s, d, deps, dryRun))
	}
	return results
}
