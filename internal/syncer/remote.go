// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package syncer

import (
	"context"
	"strings"

	"gsyncer/internal/config"
)

// shellQuote wraps s in single quotes for safe interpolation into a remote
// /bin/sh command line. An embedded single quote is escaped by closing the
// quoted run, emitting a backslash-escaped quote, and reopening it — the
// standard POSIX idiom, since sh has no escape inside single quotes.
//
// remote_path is already rejected at config load if it contains a NUL or
// control character, so quoting is the only remaining exposure — but the path
// still reaches a remote shell verbatim, and the command it lands in deletes
// directories, so it gets quoted rather than trusted.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remotePruneCmd builds the shell command that removes directories left empty
// under root by --remove-source-files.
//
// rsync deletes source *files* only; the directories that held them stay behind.
// In the motivating case — one dated directory per day of camera footage — that
// means the remote accumulates an empty dir per day forever, so the cleanup that
// makes rsync's deletion complete belongs here rather than in a user's hook.
//
// The command is deliberately narrow:
//
//   - `-mindepth 1` protects root itself, so a fully-drained tree leaves the
//     configured remote_path in place rather than deleting the directory the
//     next run is about to sync from.
//   - `-depth` visits children before parents, so a nest of empty dirs collapses
//     in one pass without a repeat run.
//   - `-type d -empty -delete` touches only directories that are already empty:
//     any file rsync did not transfer (excluded by an ignore rule, or failed)
//     keeps its parent directory alive, so this can never remove un-backed-up
//     data.
//   - `-xdev` stays on one filesystem, so a mount point under root is left for
//     its own tooling to manage.
func remotePruneCmd(root string) string {
	return "find " + shellQuote(root) + " -xdev -mindepth 1 -depth -type d -empty -delete"
}

// pruneRemoteEmptyDirs removes the now-empty directories left on the remote by
// --remove-source-files. It is called only after a transfer that actually
// succeeded, and never during a dry run — see SyncOne.
//
// A failure here is reported by the caller as a warning, not an error: the data
// is already safely local, and an empty directory left behind costs an inode,
// not a backup.
func pruneRemoteEmptyDirs(ctx context.Context, s config.Sync, port int, deps Deps) error {
	dest := s.User + "@" + s.Host
	args := append(sshBaseArgs(s.Identity, port, s.StrictHostKey, deps.KnownHostsFile),
		dest, remotePruneCmd(s.RemotePath))
	out, err := deps.Runner.Run(ctx, "ssh", args...)
	if err != nil {
		deps.Log.Errorf("[%s] remote empty-dir cleanup failed: %v: %s", s.Name, err, strings.TrimSpace(out.Stderr))
	}
	return err
}
