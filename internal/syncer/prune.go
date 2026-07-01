package syncer

import (
	"context"
	"path/filepath"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/retention"
	"gsyncer/internal/snapshot"
)

// prunePlan resolves the backend and the exact set of snapshots the retention
// policy would delete for one entry. PruneOne and CountPrunable both go through
// it so the count shown to the user always matches what is actually deleted.
func prunePlan(ctx context.Context, s config.Sync, d config.Defaults, deps Deps) (snapshot.Backend, []time.Time, error) {
	be := snapshot.Detect(ctx, s.LocalPath, deps.Runner, deps.FSType)
	times, err := be.List(s.LocalPath)
	if err != nil {
		return be, nil, err
	}
	_, del := retention.Partition(times, toPolicy(s.EffectiveRetention(d)))
	return be, del, nil
}

// CountPrunable reports how many snapshots PruneOne would delete for one entry,
// without deleting anything.
func CountPrunable(ctx context.Context, s config.Sync, d config.Defaults, deps Deps) (int, error) {
	_, del, err := prunePlan(ctx, s, d, deps)
	return len(del), err
}

// PruneOne applies the retention policy to one entry without syncing. When
// dryRun is true it logs the snapshots that would be deleted and skips every
// Delete, so an operator can preview an irreversible prune before running it;
// res.Pruned then counts the would-be deletions.
func PruneOne(ctx context.Context, s config.Sync, d config.Defaults, deps Deps, dryRun bool) Result {
	res := Result{Name: s.Name}
	be, del, err := prunePlan(ctx, s, d, deps)
	res.Mode = be.Name()
	if err != nil {
		deps.Log.Errorf("[%s] list snapshots: %v", s.Name, err)
		res.Err = err
		return res
	}
	for _, t := range del {
		p := filepath.Join(s.LocalPath, "snapshots", t.Format(snapshot.TSLayout))
		if dryRun {
			deps.Log.Infof("[%s] would prune %s", s.Name, p)
			res.Pruned++
			continue
		}
		if err := be.Delete(ctx, p); err != nil {
			deps.Log.Errorf("[%s] prune %s: %v", s.Name, p, err)
			continue
		}
		res.Pruned++
	}
	if dryRun {
		deps.Log.Infof("[%s] would prune %d snapshots", s.Name, res.Pruned)
	} else {
		deps.Log.Infof("[%s] pruned %d snapshots", s.Name, res.Pruned)
	}
	res.OK = true
	return res
}
