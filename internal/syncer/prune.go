package syncer

import (
	"context"
	"path/filepath"

	"gsync/internal/config"
	"gsync/internal/retention"
	"gsync/internal/snapshot"
)

// PruneOne applies the retention policy to one entry without syncing.
func PruneOne(ctx context.Context, s config.Sync, d config.Defaults, deps Deps) Result {
	res := Result{Name: s.Name}
	be := snapshot.Detect(ctx, s.LocalPath, deps.Runner, deps.FSType)
	res.Mode = be.Name()
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
