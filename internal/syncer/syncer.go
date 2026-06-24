// Package syncer orchestrates the per-entry pipeline: preflight, rsync pull,
// snapshot, and retention prune.
package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/retention"
	"gsync/internal/snapshot"
)

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
}

// Result is the outcome of syncing one entry.
type Result struct {
	Name     string
	OK       bool
	Err      error
	Files    int64
	Bytes    int64
	Snapshot string
	Mode     string
	Pruned   int
}

func toPolicy(r config.Retention) retention.Policy {
	return retention.Policy{
		Recent: r.Recent, Monthly: r.Monthly,
		Semiannual: r.Semiannual, Yearly: r.Yearly,
	}
}

// SyncOne runs the full pipeline for a single entry. It never panics; failures
// are reported via Result.Err with res.OK == false.
func SyncOne(ctx context.Context, s config.Sync, d config.Defaults, deps Deps, dryRun bool) Result {
	res := Result{Name: s.Name}
	port := s.EffectivePort(d)

	if _, err := deps.Runner.Run(ctx, "rsync", "--version"); err != nil {
		deps.Log.Errorf("[%s] local rsync missing: %s", s.Name, installHint())
		res.Err = err
		return res
	}
	if _, err := deps.Runner.Run(ctx, "ssh",
		sshCmdArgs(s.Identity, port, s.StrictHostKey, s.User, s.Host, "command -v rsync")...); err != nil {
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

	out, err := deps.Runner.Run(ctx, "rsync", buildRsyncArgs(s, port, cur, dryRun)...)
	if err != nil {
		deps.Log.Errorf("[%s] rsync failed: %v: %s", s.Name, err, out.Stderr)
		res.Err = err
		return res
	}
	res.Files, res.Bytes = parseStats(out.Stdout)
	deps.Log.Infof("[%s] pulled %d files, %d bytes", s.Name, res.Files, res.Bytes)

	if dryRun {
		res.OK = true
		return res
	}

	ts := deps.Now()
	snap, err := be.Create(ctx, s.LocalPath, ts)
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
		results = append(results, SyncOne(ctx, s, d, deps, dryRun))
	}
	return results
}
