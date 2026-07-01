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

func TestBuildRsyncArgs(t *testing.T) {
	s := config.Sync{
		User: "u", Host: "h", RemotePath: "/src", Ignore: []string{"*.log"},
	}
	args := buildRsyncArgs(s, 22, "/local/current", false, false, "/cfg/known_hosts")
	j := strings.Join(args, " ")
	if !strings.Contains(j, "-a") || !strings.Contains(j, "--delete") ||
		!strings.Contains(j, "--info=stats2") || !strings.Contains(j, "--timeout 300") {
		t.Fatalf("missing base flags: %v", args)
	}
	if !contains(args, "-s") {
		t.Fatalf("missing --protect-args (-s): %v", args)
	}
	if !contains(args, "--numeric-ids") {
		t.Fatalf("missing --numeric-ids: %v", args)
	}
	if !contains(args, "--partial") || !contains(args, "--partial-dir=.gsync-partial") {
		t.Fatalf("missing --partial / --partial-dir: %v", args)
	}
	if !strings.Contains(j, "UserKnownHostsFile=/cfg/known_hosts") {
		t.Fatalf("known_hosts not threaded into -e: %v", args)
	}
	// The partial-dir exclude must precede the user's own filters (rsync is
	// first-match-wins) so nothing can pull the staging dir into the mirror.
	if !strings.Contains(j, "--filter - /.gsync-partial/") {
		t.Fatalf("missing partial-dir exclude filter: %v", args)
	}
	if strings.Index(j, "--filter - /.gsync-partial/") > strings.Index(j, "--filter - *.log") {
		t.Fatalf("partial-dir exclude must come before user filters: %v", args)
	}
	if !strings.Contains(j, "--filter - *.log") {
		t.Fatalf("missing filter: %v", args)
	}
	if !strings.Contains(j, "u@h:/src/") || !strings.Contains(j, "/local/current/") {
		t.Fatalf("missing src/dst with trailing slash: %v", args)
	}
	if contains(args, "-z") {
		t.Fatalf("compress should be off by default: %v", args)
	}
	if strings.Contains(j, " -n") {
		t.Fatalf("dry-run should be off: %v", args)
	}
	dry := buildRsyncArgs(s, 22, "/local/current", true, false, "/cfg/known_hosts")
	if !contains(dry, "-n") {
		t.Fatalf("dry-run flag missing: %v", dry)
	}
	// compress=true adds -z.
	comp := buildRsyncArgs(s, 22, "/local/current", false, true, "/cfg/known_hosts")
	if !contains(comp, "-z") {
		t.Fatalf("compress flag -z missing when enabled: %v", comp)
	}
}

func TestEffectiveCompress(t *testing.T) {
	tru, fls := true, false
	// entry override wins in both directions
	if !(config.Sync{Compress: &tru}).EffectiveCompress(config.Defaults{Compress: false}) {
		t.Fatal("entry compress=true should win over default false")
	}
	if (config.Sync{Compress: &fls}).EffectiveCompress(config.Defaults{Compress: true}) {
		t.Fatal("entry compress=false should win over default true")
	}
	// no override -> falls back to default
	if !(config.Sync{}).EffectiveCompress(config.Defaults{Compress: true}) {
		t.Fatal("nil override should fall back to default true")
	}
	if (config.Sync{}).EffectiveCompress(config.Defaults{Compress: false}) {
		t.Fatal("nil override should fall back to default false")
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
