package syncer

import (
	"fmt"
	"strconv"
	"strings"

	"gsync/internal/config"
	"gsync/internal/ignore"
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

// sshOptArg builds the single string passed to rsync's -e option.
func sshOptArg(identity string, port int, strict bool) string {
	parts := []string{"ssh", "-p", strconv.Itoa(port), "-o", "BatchMode=yes", "-o", strictOpt(strict)}
	if identity != "" {
		parts = append(parts, "-i", config.ExpandHome(identity))
	}
	return strings.Join(parts, " ")
}

// sshCmdArgs builds args for invoking ssh directly (used by preflight).
func sshCmdArgs(identity string, port int, strict bool, user, host, remoteCmd string) []string {
	args := []string{"-p", strconv.Itoa(port), "-o", "BatchMode=yes", "-o", strictOpt(strict)}
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

// buildRsyncArgs assembles the full rsync argument list for one entry.
func buildRsyncArgs(s config.Sync, port int, currentPath string, dryRun bool) []string {
	args := []string{"-a", "--delete", "--info=stats2"}
	if dryRun {
		args = append(args, "-n")
	}
	for _, f := range ignore.ToRsyncFilters(s.Ignore) {
		args = append(args, "--filter", f)
	}
	args = append(args, "-e", sshOptArg(s.Identity, port, s.StrictHostKey))
	src := fmt.Sprintf("%s@%s:%s", s.User, s.Host, ensureTrailingSlash(s.RemotePath))
	args = append(args, src, ensureTrailingSlash(currentPath))
	return args
}

// installHint returns a multi-package-manager hint for installing rsync.
func installHint() string {
	return "install rsync, e.g.: apt install rsync | dnf install rsync | " +
		"yum install rsync | apk add rsync | pacman -S rsync"
}
