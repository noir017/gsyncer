package syncer

import (
	"fmt"
	"strconv"
	"strings"

	"gsync/internal/config"
	"gsync/internal/ignore"
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

// sshOptArg builds the single string passed to rsync's -e option.
func sshOptArg(identity string, port int, strict bool, knownHosts string) string {
	parts := []string{"ssh", "-p", strconv.Itoa(port), "-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(sshConnectTimeout), "-o", strictOpt(strict)}
	parts = append(parts, knownHostsOpts(knownHosts)...)
	if identity != "" {
		parts = append(parts, "-i", config.ExpandHome(identity))
	}
	return strings.Join(parts, " ")
}

// sshCmdArgs builds args for invoking ssh directly (used by preflight).
func sshCmdArgs(identity string, port int, strict bool, knownHosts, user, host, remoteCmd string) []string {
	args := []string{"-p", strconv.Itoa(port), "-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(sshConnectTimeout), "-o", strictOpt(strict)}
	args = append(args, knownHostsOpts(knownHosts)...)
	if identity != "" {
		args = append(args, "-i", config.ExpandHome(identity))
	}
	args = append(args, fmt.Sprintf("%s@%s", user, host), remoteCmd)
	return args
}

// ensureTrailingSlash guarantees a trailing slash (rsync dir-content semantics).
func ensureTrailingSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// buildRsyncArgs assembles the full rsync argument list for one entry. The -s
// (--protect-args) flag stops the remote shell from a second round of
// word-splitting/globbing on the remote path, so spaces or shell metacharacters
// in remote_path are transferred literally instead of being re-interpreted.
func buildRsyncArgs(s config.Sync, port int, currentPath string, dryRun bool, knownHosts string, bwlimit int) []string {
	// stats2 feeds parseStats (Files/Bytes); progress2 emits an aggregate
	// progress line that RunStream forwards live to the run screen.
	args := []string{"-a", "-s", "--delete", "--info=stats2,progress2", "--timeout", strconv.Itoa(rsyncIOTimeout)}
	if dryRun {
		args = append(args, "-n")
	}
	// Throttle transfer rate when configured (KB/s); a plain passthrough to
	// rsync's own --bwlimit so an unattended pull doesn't saturate a link.
	if bwlimit > 0 {
		args = append(args, "--bwlimit", strconv.Itoa(bwlimit))
	}
	for _, f := range ignore.ToRsyncFilters(s.Ignore) {
		args = append(args, "--filter", f)
	}
	args = append(args, "-e", sshOptArg(s.Identity, port, s.StrictHostKey, knownHosts))
	src := fmt.Sprintf("%s@%s:%s", s.User, s.Host, ensureTrailingSlash(s.RemotePath))
	args = append(args, src, ensureTrailingSlash(currentPath))
	return args
}

// installHint returns a multi-package-manager hint for installing rsync.
func installHint() string {
	return "install rsync, e.g.: apt install rsync | dnf install rsync | " +
		"yum install rsync | apk add rsync | pacman -S rsync"
}
