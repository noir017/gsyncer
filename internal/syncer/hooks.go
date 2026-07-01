package syncer

import (
	"context"
	"strconv"

	"gsyncer/internal/config"
)

// hookEnv builds the GSYNC_* environment exposed to pre/post-sync hooks so a
// command can act on the entry without gsyncer interpolating values into the
// shell string. extra carries phase-specific vars (e.g. the created snapshot).
func hookEnv(s config.Sync, phase string, extra []string) []string {
	env := []string{
		"GSYNC_PHASE=" + phase,
		"GSYNC_NAME=" + s.Name,
		"GSYNC_HOST=" + s.Host,
		"GSYNC_USER=" + s.User,
		"GSYNC_REMOTE_PATH=" + s.RemotePath,
		"GSYNC_LOCAL_PATH=" + s.LocalPath,
	}
	return append(env, extra...)
}

// runHook executes one hook command via `sh -c` with the hook environment. A
// nonempty error is returned so the caller can decide whether to abort (pre) or
// merely warn (post). An empty command is a no-op.
func runHook(ctx context.Context, deps Deps, s config.Sync, phase, cmd string, extra []string) error {
	if cmd == "" {
		return nil
	}
	deps.Log.Infof("[%s] %s hook: %s", s.Name, phase, cmd)
	out, err := deps.Runner.RunEnv(ctx, hookEnv(s, phase, extra), "sh", "-c", cmd)
	if err != nil {
		deps.Log.Errorf("[%s] %s hook failed: %v: %s", s.Name, phase, err, out.Stderr)
	}
	return err
}

// postSyncEnv is the extra environment for the post-sync hook.
func postSyncEnv(res Result) []string {
	return []string{
		"GSYNC_SNAPSHOT=" + res.Snapshot,
		"GSYNC_FILES=" + strconv.FormatInt(res.Files, 10),
		"GSYNC_BYTES=" + strconv.FormatInt(res.Bytes, 10),
	}
}
