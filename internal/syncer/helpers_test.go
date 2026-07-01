package syncer

import (
	"strings"
	"testing"

	"gsync/internal/config"
)

func TestParseStats(t *testing.T) {
	out := `
Number of files: 100
Number of regular files transferred: 12
Total file size: 999 bytes
Total transferred file size: 3456 bytes
`
	files, bytes := parseStats(out)
	if files != 12 || bytes != 3456 {
		t.Fatalf("files=%d bytes=%d", files, bytes)
	}
}

func TestSSHOptArg(t *testing.T) {
	got := sshOptArg("/k", 2222, false, "/cfg/known_hosts")
	if !strings.Contains(got, "ssh -p 2222") ||
		!strings.Contains(got, "-i /k") ||
		!strings.Contains(got, "BatchMode=yes") ||
		!strings.Contains(got, "ConnectTimeout=10") ||
		!strings.Contains(got, "StrictHostKeyChecking=accept-new") ||
		!strings.Contains(got, "UserKnownHostsFile=/cfg/known_hosts") ||
		!strings.Contains(got, "GlobalKnownHostsFile=/dev/null") {
		t.Fatalf("opt arg = %q", got)
	}
	strict := sshOptArg("/k", 22, true, "/cfg/known_hosts")
	if !strings.Contains(strict, "StrictHostKeyChecking=yes") {
		t.Fatalf("strict opt = %q", strict)
	}
	// No known_hosts configured -> neither option should appear.
	none := sshOptArg("/k", 22, false, "")
	if strings.Contains(none, "UserKnownHostsFile") || strings.Contains(none, "GlobalKnownHostsFile") {
		t.Fatalf("unexpected known_hosts opts = %q", none)
	}
}

func TestSSHCmdArgs(t *testing.T) {
	args := sshCmdArgs("/k", 22, false, "/cfg/known_hosts", "u", "h", "command -v rsync")
	j := strings.Join(args, " ")
	if !strings.Contains(j, "-p 22") || !strings.Contains(j, "-i /k") ||
		!strings.Contains(j, "ConnectTimeout=10") ||
		!strings.Contains(j, "UserKnownHostsFile=/cfg/known_hosts") ||
		!strings.Contains(j, "GlobalKnownHostsFile=/dev/null") ||
		!strings.HasSuffix(j, "u@h command -v rsync") {
		t.Fatalf("cmd args = %v", args)
	}
}

func TestBuildRsyncArgs(t *testing.T) {
	s := config.Sync{
		User: "u", Host: "h", RemotePath: "/src", Ignore: []string{"*.log"},
	}
	args := buildRsyncArgs(s, 22, "/local/current", false, "/cfg/known_hosts", 0)
	j := strings.Join(args, " ")
	if !strings.Contains(j, "-a") || !strings.Contains(j, "--delete") ||
		!strings.Contains(j, "--info=stats2") || !strings.Contains(j, "--timeout 300") {
		t.Fatalf("missing base flags: %v", args)
	}
	if !contains(args, "-s") {
		t.Fatalf("missing --protect-args (-s): %v", args)
	}
	if !strings.Contains(j, "UserKnownHostsFile=/cfg/known_hosts") {
		t.Fatalf("known_hosts not threaded into -e: %v", args)
	}
	if !strings.Contains(j, "--filter - *.log") {
		t.Fatalf("missing filter: %v", args)
	}
	if !strings.Contains(j, "u@h:/src/") || !strings.Contains(j, "/local/current/") {
		t.Fatalf("missing src/dst with trailing slash: %v", args)
	}
	if strings.Contains(j, " -n") {
		t.Fatalf("dry-run should be off: %v", args)
	}
	if strings.Contains(j, "--bwlimit") {
		t.Fatalf("bwlimit 0 should add no flag: %v", args)
	}
	dry := buildRsyncArgs(s, 22, "/local/current", true, "/cfg/known_hosts", 0)
	if !contains(dry, "-n") {
		t.Fatalf("dry-run flag missing: %v", dry)
	}
}

func TestBuildRsyncArgsBwlimit(t *testing.T) {
	s := config.Sync{User: "u", Host: "h", RemotePath: "/src"}
	args := buildRsyncArgs(s, 22, "/local/current", false, "", 500)
	if !strings.Contains(strings.Join(args, " "), "--bwlimit 500") {
		t.Fatalf("bwlimit not passed through: %v", args)
	}
}

func TestEffectiveBwlimit(t *testing.T) {
	d := config.Defaults{Bwlimit: 100}
	if got := (config.Sync{}).EffectiveBwlimit(d); got != 100 {
		t.Fatalf("inherit default: got %d, want 100", got)
	}
	if got := (config.Sync{Bwlimit: 250}).EffectiveBwlimit(d); got != 250 {
		t.Fatalf("entry override: got %d, want 250", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
