// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package syncer

import (
	"fmt"
	"strconv"
	"strings"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/ignore"
)

const (
	// sshConnectTimeout bounds the TCP/handshake phase so an unreachable host
	// fails fast instead of hanging a cron run forever.
	sshConnectTimeout = 10
	// rsyncIOTimeout aborts a transfer that stalls (no I/O) for this many
	// seconds, guarding against half-open connections.
	rsyncIOTimeout = 300
)

// parseStats extracts the transferred file count and byte count from rsync's
// --info=stats2 / --stats output.
func parseStats(out string) (files, bytes int64) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "regular files transferred:"),
			strings.Contains(line, "files transferred:"):
			files = lastInt(line)
		case strings.Contains(line, "Total transferred file size:"):
			bytes = lastInt(line)
		}
	}
	return files, bytes
}

// lastInt returns the last run of digits in a line as an int64 (0 if none).
func lastInt(line string) int64 {
	var digits strings.Builder
	var last string
	for _, r := range line {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		} else if digits.Len() > 0 {
			last = digits.String()
			digits.Reset()
		}
	}
	if digits.Len() > 0 {
		last = digits.String()
	}
	if last == "" {
		return 0
	}
	n, _ := strconv.ParseInt(last, 10, 64)
	return n
}

func strictOpt(strict bool) string {
	if strict {
		return "StrictHostKeyChecking=yes"
	}
	return "StrictHostKeyChecking=accept-new"
}

// knownHostsOpts pins the known_hosts store so host keys learned on the first
// contact persist across (cron) runs — without this the store is effectively
// empty every run and accept-new trusts on first sight every time, leaving the
// --delete mirror open to a MITM feeding forged content. GlobalKnownHostsFile
// is disabled so only our per-config store is consulted. Returned as flat
// -o pairs; empty when no path is configured.
func knownHostsOpts(knownHosts string) []string {
	if knownHosts == "" {
		return nil
	}
	return []string{
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null",
	}
}

// sshBaseArgs builds the ssh options shared by the rsync transport (-e) and the
// standalone ssh calls used for remote cleanup, so both speak to the host with
// the same identity, port, timeout, and host-key policy.
func sshBaseArgs(identity string, port int, strict bool, knownHosts string) []string {
	args := []string{"-p", strconv.Itoa(port), "-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(sshConnectTimeout), "-o", strictOpt(strict)}
	args = append(args, knownHostsOpts(knownHosts)...)
	if identity != "" {
		args = append(args, "-i", config.ExpandHome(identity))
	}
	return args
}

// sshOptArg builds the single string passed to rsync's -e option.
func sshOptArg(identity string, port int, strict bool, knownHosts string) string {
	return strings.Join(append([]string{"ssh"}, sshBaseArgs(identity, port, strict, knownHosts)...), " ")
}

// ensureTrailingSlash guarantees a trailing slash (rsync dir-content semantics).
func ensureTrailingSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// partialDir is the in-transfer staging directory (under current/) that rsync
// --partial-dir uses. Keeping half-written files here instead of the current/
// root means an interrupted large file never pollutes current/ or a snapshot.
const partialDir = ".gsyncer-partial"

// rsyncOpts carries the per-entry knobs that are resolved from config (entry
// override over defaults) rather than read straight off config.Sync. It is a
// struct so adding a knob does not grow buildRsyncArgs' parameter list into an
// unreadable row of bare bools at every call site.
type rsyncOpts struct {
	dryRun   bool
	compress bool
	bwlimit  int
	// archive drops --delete so current/ accumulates instead of tracking the
	// remote; see config.LocalModeArchive.
	archive bool
	// removeSource adds --remove-source-files, letting rsync delete each remote
	// file it has *successfully* transferred. Using rsync's own accounting is the
	// point: it knows exactly which transfers succeeded, which no external
	// file-list-and-timestamp scheme can reconstruct accurately.
	removeSource bool
}

// buildRsyncArgs assembles the full rsync argument list for one entry. The -s
// (--protect-args) flag stops the remote shell from a second round of
// word-splitting/globbing on the remote path, so spaces or shell metacharacters
// in remote_path are transferred literally instead of being re-interpreted.
//
// --partial + --partial-dir let an interrupted transfer resume instead of
// restarting from scratch (pairs with the 300s I/O timeout), while confining the
// in-flight temp files to partialDir; an explicit exclude keeps that dir out of
// the mirror. --numeric-ids preserves uid/gid verbatim (a faithful backup, and
// no remote name lookups). -z (compress) is opt-in per entry / default; bwlimit
// throttles the transfer rate (KB/s) when configured.
//
// --delete is present only in mirror mode: an archive must never let a file
// disappearing from the remote (which, with removeSource, is every file gsyncer
// itself just pulled) propagate into current/.
func buildRsyncArgs(s config.Sync, port int, currentPath string, knownHosts string, o rsyncOpts) []string {
	// stats2 feeds parseStats (Files/Bytes); progress2 emits an aggregate
	// progress line that RunStream forwards live to the run screen.
	args := []string{"-a", "-s", "--numeric-ids",
		"--partial", "--partial-dir=" + partialDir,
		"--info=stats2,progress2", "--timeout", strconv.Itoa(rsyncIOTimeout)}
	if !o.archive {
		args = append(args, "--delete")
	}
	if o.removeSource {
		// rsync deletes a source file only after that file's transfer is verified,
		// and -n makes the whole run a no-op, so a dry-run preview stays read-only
		// on the remote as well as locally.
		args = append(args, "--remove-source-files")
	}
	if o.compress {
		args = append(args, "-z")
	}
	if o.dryRun {
		args = append(args, "-n")
	}
	// Throttle transfer rate when configured (KB/s); a plain passthrough to
	// rsync's own --bwlimit so an unattended pull doesn't saturate a link.
	if o.bwlimit > 0 {
		args = append(args, "--bwlimit", strconv.Itoa(o.bwlimit))
	}
	// Exclude the partial-dir first (rsync is first-match-wins) so no user rule
	// can pull the in-transfer staging dir into the mirror.
	args = append(args, "--filter", "- /"+partialDir+"/")
	for _, f := range ignore.ToRsyncFilters(s.Ignore) {
		args = append(args, "--filter", f)
	}
	args = append(args, "-e", sshOptArg(s.Identity, port, s.StrictHostKey, knownHosts))
	src := fmt.Sprintf("%s@%s:%s", s.User, s.Host, ensureTrailingSlash(s.RemotePath))
	args = append(args, src, ensureTrailingSlash(currentPath))
	return args
}

// remoteRsyncMissing reports whether a failed rsync run looks like the remote
// host is missing the rsync binary. When the remote shell can't find rsync it
// prints "command not found" and the client exits 127, so we can surface the
// same install hint the (now removed) preflight probe used to give.
func remoteRsyncMissing(out execx.Result) bool {
	if out.Code == 127 {
		return true
	}
	e := out.Stderr
	return strings.Contains(e, "command not found") || strings.Contains(e, "rsync: not found")
}

// installHint returns a multi-package-manager hint for installing rsync.
func installHint() string {
	return "install rsync, e.g.: apt install rsync | dnf install rsync | " +
		"yum install rsync | apk add rsync | pacman -S rsync"
}
