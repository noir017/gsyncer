// Package syncer orchestrates the per-entry pipeline: preflight, rsync pull,
// snapshot, and retention prune.
package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/retention"
	"gsyncer/internal/snapshot"
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

var (
	rsyncProbeOnce sync.Once
	rsyncProbeErr  error
)

// checkLocalRsync runs the local `rsync --version` preflight. For the real
// runner the result is memoized for the life of the process — whether rsync is
// installed is a fixed host property, and a parallel SyncMany would otherwise
// spawn one redundant probe process per entry (mirrors snapshot's memoized
// btrfs probe). Other Runner implementations (test fakes) are probed every
// call so each test can script its own outcome.
func checkLocalRsync(ctx context.Context, r execx.Runner) error {
	if _, isReal := r.(execx.Real); isReal {
		rsyncProbeOnce.Do(func() {
			_, rsyncProbeErr = r.Run(ctx, "rsync", "--version")
		})
		return rsyncProbeErr
	}
	_, err := r.Run(ctx, "rsync", "--version")
	return err
}

// Logger is the subset of logging the syncer needs.
type Logger interface {
	Infof(format string, a ...any)
	Errorf(format string, a ...any)
}

// lockedLogger serializes writes to an underlying Logger so concurrent SyncOne
// goroutines (see SyncMany) don't race on the shared RunLogger/chanLogger. Each
// Infof/Errorf emits one line, so a single mutex around the call is sufficient.
type lockedLogger struct {
	mu    *sync.Mutex
	inner Logger
}

func (l *lockedLogger) Infof(format string, a ...any) {
	l.mu.Lock()
	l.inner.Infof(format, a...)
	l.mu.Unlock()
}

func (l *lockedLogger) Errorf(format string, a ...any) {
	l.mu.Lock()
	l.inner.Errorf(format, a...)
	l.mu.Unlock()
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
	// If the run was already cancelled (e.g. ctrl+c while this entry waited for a
	// concurrency slot), skip cleanly rather than letting preflight fail against a
	// dead context and report a spurious FAILED.
	if ctx.Err() != nil {
		res.Skipped = true
		return res
	}
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

	// Pre-sync hook runs before any rsync work. A failure skips this entry's
	// rsync entirely so a half-prepared source never produces a torn backup.
	// Hooks are side-effecting, so a dry-run preview does not run them.
	if !dryRun {
		if err := runHook(ctx, deps, s, "pre_sync", s.EffectivePreSync(d), nil); err != nil {
			res.Err = err
			return res
		}
	}

	if err := checkLocalRsync(ctx, deps.Runner); err != nil {
		deps.Log.Errorf("[%s] local rsync missing: %s", s.Name, installHint())
		res.Err = err
		return res
	}
	// No separate remote `command -v rsync` probe: it cost a full extra ssh
	// handshake per entry, and rsync's own run already fails clearly when the
	// remote rsync is missing (exit 127 / "command not found"), which we detect
	// below and annotate with the same install hint.

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

	out, err := runRsync(ctx, deps, s.Name, buildRsyncArgs(s, port, cur, dryRun, s.EffectiveCompress(d), deps.KnownHostsFile, s.EffectiveBwlimit(d)))
	if err != nil {
		if rsyncPartialWarning(out.Code) {
			// 23/24 mean the transfer mostly succeeded (some files failed or
			// vanished mid-copy — routine on a live source). Treat as a warning
			// and still snapshot, rather than skipping the backup entirely.
			deps.Log.Errorf("[%s] rsync completed with warnings (exit %d), continuing: %s", s.Name, out.Code, out.Stderr)
		} else {
			if remoteRsyncMissing(out) {
				deps.Log.Errorf("[%s] remote rsync missing on %s: %s", s.Name, s.Host, installHint())
			}
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
	// The hardlink backend upgrades to reflink copies on CoW filesystems; surface
	// the technique actually used (reflink vs hardlink) now that Create has run.
	if mr, ok := be.(interface{ Mode() string }); ok {
		res.Mode = mr.Mode()
	}
	deps.Log.Infof("[%s] snapshot created: %s (%s)", s.Name, snap, res.Mode)

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

	// Post-sync hook runs only after a successful snapshot+prune. Its failure is
	// a warning: the backup already exists, so it does not flip the result.
	if err := runHook(ctx, deps, s, "post_sync", s.EffectivePostSync(d), postSyncEnv(res)); err != nil {
		deps.Log.Errorf("[%s] post_sync hook failed (backup already taken): %v", s.Name, err)
	}

	res.OK = true
	return res
}

// SyncMany runs entries with bounded concurrency, isolating per-entry failures.
// At most jobs entries run at once (jobs <= 0 is treated as 1). Results are
// written to a fixed-length slice by index, so the returned order always matches
// the input order regardless of completion order. Entries not launched because
// the run was cancelled are reported as Skipped rather than omitted.
//
// Per-entry isolation still comes from the per-local_path flock in SyncOne; two
// entries sharing a local_path simply serialize (the loser is Skipped). The only
// shared mutable state across goroutines is deps.Log, which is wrapped in a
// mutex for jobs > 1.
func SyncMany(ctx context.Context, entries []config.Sync, d config.Defaults, deps Deps, dryRun bool, jobs int) []Result {
	results := make([]Result, len(entries))
	if jobs < 1 {
		jobs = 1
	}

	runDeps := deps
	if jobs > 1 {
		runDeps.Log = &lockedLogger{mu: &sync.Mutex{}, inner: deps.Log}
	}

	var g errgroup.Group
	g.SetLimit(jobs)
	cancelled := 0
	for i, s := range entries {
		if ctx.Err() != nil {
			// The run was cancelled (ctrl+c) or timed out. Don't launch further
			// entries against a dead context; mark them Skipped so callers can tell
			// them apart from real failures.
			results[i] = Result{Name: s.Name, Skipped: true}
			cancelled++
			continue
		}
		g.Go(func() error {
			results[i] = SyncOne(ctx, s, d, runDeps, dryRun)
			return nil
		})
	}
	_ = g.Wait()
	if cancelled > 0 {
		deps.Log.Errorf("run cancelled; %d entr(ies) skipped", cancelled)
	}
	return results
}
