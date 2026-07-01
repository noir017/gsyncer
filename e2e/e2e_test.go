//go:build e2e

// End-to-end tests that drive the compiled gsync binary through its real
// ssh+rsync pipeline against live servers. They are excluded from the default
// `go test ./...` by the `e2e` build tag, since they need network access and
// SSH credentials the unit suite must not depend on.
//
// The servers are supplied by a local config file (never committed), so no host
// names or credentials live in the source tree. See e2e.config.example.toml for
// the format. By default the tests read ./e2e.config.toml (relative to this
// package); override the path with GSYNC_E2E_CONFIG. When no config file exists,
// or it lists no matching servers, the affected tests skip cleanly.
//
// Run:
//
//	cp e2e.config.example.toml e2e.config.toml   # then edit in your servers
//	go test -tags e2e ./e2e -v
//
// Requirements: `ssh` and `rsync` on the machine running the tests, plus the
// SSH access described by your config file. Each test provisions a unique
// remote temp directory and removes it when done. The tests are black-box: they
// build and exec the binary from the module root (..), so nothing here depends
// on the module's import path or internal packages.
package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/BurntSushi/toml"
)

// gsyncBin is the freshly compiled binary shared by all e2e tests.
var gsyncBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gsync-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: mkdtemp:", err)
		os.Exit(1)
	}
	gsyncBin = filepath.Join(dir, "gsync")
	// Build the same static binary users deploy, from the module root (the parent
	// of this package). Using ".." rather than an import path keeps the tests
	// working even if the module is renamed on a fork.
	build := exec.Command("go", "build", "-o", gsyncBin, "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build failed: %v\n%s", err, out)
		os.Remove(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// e2eServer is one SSH target from the config file. Identity and RemoteBase are
// optional: an empty Identity falls back to ssh's own config (~/.ssh/config),
// and an empty RemoteBase defaults to /tmp.
type e2eServer struct {
	Host       string `toml:"host"`
	User       string `toml:"user"`
	Port       int    `toml:"port"`
	Identity   string `toml:"identity"`
	RemoteBase string `toml:"remote_base"`
}

// e2eConfig is the whole e2e config file. `server` entries are rsync-capable
// hosts the full pipeline runs against (zero or more); `no_rsync_server` entries
// are hosts deliberately lacking rsync, used to exercise the failure path.
type e2eConfig struct {
	Server        []e2eServer `toml:"server"`
	NoRsyncServer []e2eServer `toml:"no_rsync_server"`
}

// loadE2EConfig reads the config file named by GSYNC_E2E_CONFIG, or
// ./e2e.config.toml by default. A missing file yields an empty config (so tests
// skip rather than fail) rather than an error.
func loadE2EConfig(t *testing.T) e2eConfig {
	t.Helper()
	path := os.Getenv("GSYNC_E2E_CONFIG")
	if path == "" {
		path = "e2e.config.toml"
	}
	var cfg e2eConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return e2eConfig{}
		}
		t.Fatalf("e2e config %s: %v", path, err)
	}
	return cfg
}

// remote is a resolved server: port as a string and defaults applied.
type remote struct {
	host, user, port, identity, remoteBase string
}

func (s e2eServer) remote() remote {
	port := s.Port
	if port == 0 {
		port = 22
	}
	base := s.RemoteBase
	if base == "" {
		base = "/tmp"
	}
	return remote{host: s.Host, user: s.User, port: strconv.Itoa(port),
		identity: s.Identity, remoteBase: base}
}

// label is a filesystem/subtest-safe name for the server (its host).
func (r remote) label() string {
	return strings.NewReplacer("/", "_", " ", "_", ":", "_").Replace(r.host)
}

// sshArgs builds the argument list for an ssh invocation to this host, applying
// the same batch/timeout/identity options gsync itself uses.
func (r remote) sshArgs(command ...string) []string {
	a := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-p", r.port}
	if r.identity != "" {
		a = append(a, "-i", expandHome(r.identity))
	}
	a = append(a, r.user+"@"+r.host)
	return append(a, command...)
}

// ssh runs a command on the remote, failing the test on error. Fixture
// setup/teardown goes through this, independent of gsync.
func (r remote) ssh(t *testing.T, command string) string {
	t.Helper()
	out, err := exec.Command("ssh", r.sshArgs(command)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh %s@%s %q: %v\n%s", r.user, r.host, command, err, out)
	}
	return string(out)
}

// reachable reports whether the remote answers a trivial SSH command; used to
// skip cleanly when a configured host is unavailable.
func (r remote) reachable() bool {
	return exec.Command("ssh", r.sshArgs("true")...).Run() == nil
}

// runResult is one invocation of the gsync binary.
type runResult struct {
	stdout, stderr string
	code           int
}

// gsync runs the compiled binary with args, using workDir as its directory (so
// logs land under workDir/logs, isolated per test). It never fails the test —
// callers assert on code/stdout so both success and failure paths are testable.
func gsync(t *testing.T, workDir string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(gsyncBin, args...)
	cmd.Dir = workDir
	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("gsync %v: %v", args, err)
		}
	}
	return runResult{stdout: so.String(), stderr: se.String(), code: code}
}

// writeConfig writes a single-entry gsync config for the given remote and
// returns its path. recent controls the retention keep-count; the other GFS
// layers are 0 so pruning is fully determined by recent.
func writeConfig(t *testing.T, dir string, r remote, remotePath, localPath string, recent int) string {
	t.Helper()
	identity := ""
	if r.identity != "" {
		identity = fmt.Sprintf("  identity = %q\n", r.identity)
	}
	cfg := fmt.Sprintf(`[defaults]
  ssh_port = %s
  [defaults.retention]
    recent = %d
    monthly = 0
    semiannual = 0
    yearly = 0

[[sync]]
  name = "web"
  host = "%s"
  user = "%s"
  port = %s
%s  remote_path = "%s"
  local_path = "%s"
  ignore = ["node_modules/"]
`, r.port, recent, r.host, r.user, r.port, identity, remotePath, localPath)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// snapNames returns the snapshot directory names under localPath, sorted by
// ReadDir (lexical == chronological for the timestamp layout).
func snapNames(t *testing.T, localPath string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(localPath, "snapshots"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// entryStatus mirrors the JSON emitted by `gsync status --json`. Declared here
// (rather than importing the binary's type) to keep these tests black-box.
type entryStatus struct {
	Name     string  `json:"name"`
	Backend  string  `json:"backend"`
	Count    int     `json:"count"`
	Latest   string  `json:"latest"`
	AgeHours float64 `json:"age_hours"`
	Stale    bool    `json:"stale"`
}

// TestE2E walks the full lifecycle against each configured rsync-capable host.
// With no servers configured (or none reachable) it skips.
func TestE2E(t *testing.T) {
	cfg := loadE2EConfig(t)
	if len(cfg.Server) == 0 {
		t.Skip("no [[server]] in e2e config; copy e2e.config.example.toml to e2e.config.toml (or set GSYNC_E2E_CONFIG)")
	}
	for _, srv := range cfg.Server {
		r := srv.remote()
		t.Run(r.label(), func(t *testing.T) {
			if r.host == "" || r.user == "" {
				t.Fatalf("server entry needs host and user: %+v", srv)
			}
			if !r.reachable() {
				t.Skipf("server %s@%s:%s unreachable", r.user, r.host, r.port)
			}
			runPipeline(t, r)
		})
	}
}

// runPipeline provisions a remote fixture and exercises the whole gsync
// lifecycle against one host, asserting on-disk results at each step.
func runPipeline(t *testing.T, r remote) {
	// Unique remote fixture, torn down at the end.
	remotePath := fmt.Sprintf("%s/gsync-e2e-%d", strings.TrimRight(r.remoteBase, "/"), os.Getpid())
	r.ssh(t, fmt.Sprintf(
		"rm -rf %[1]s && mkdir -p %[1]s/sub %[1]s/node_modules && "+
			"printf 'hello\\n' > %[1]s/a.txt && printf 'world\\n' > %[1]s/sub/b.txt && "+
			"printf 'junk\\n' > %[1]s/node_modules/x.js", remotePath))
	t.Cleanup(func() { r.ssh(t, "rm -rf "+remotePath) })

	work := t.TempDir()
	localPath := filepath.Join(work, "data", "web")
	cfgArg := []string{"--config", writeConfig(t, work, r, remotePath, localPath, 10)}

	t.Run("check", func(t *testing.T) {
		res := gsync(t, work, append([]string{"check"}, cfgArg...)...)
		if res.code != 0 {
			t.Fatalf("check code=%d stderr=%s", res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, "config OK") {
			t.Fatalf("check stdout=%q", res.stdout)
		}
	})

	t.Run("list", func(t *testing.T) {
		res := gsync(t, work, append([]string{"list"}, cfgArg...)...)
		if res.code != 0 || !strings.Contains(res.stdout, "web") ||
			!strings.Contains(res.stdout, r.host) {
			t.Fatalf("list code=%d stdout=%q", res.code, res.stdout)
		}
	})

	t.Run("sync_and_ignore", func(t *testing.T) {
		res := gsync(t, work, append([]string{"sync"}, cfgArg...)...)
		if res.code != 0 {
			t.Fatalf("sync code=%d stdout=%q stderr=%q", res.code, res.stdout, res.stderr)
		}
		// current/ mirrors the remote, minus the ignored node_modules/.
		if b, err := os.ReadFile(filepath.Join(localPath, "current", "a.txt")); err != nil || string(b) != "hello\n" {
			t.Fatalf("current/a.txt = %q, err=%v", b, err)
		}
		if _, err := os.Stat(filepath.Join(localPath, "current", "sub", "b.txt")); err != nil {
			t.Fatalf("current/sub/b.txt missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(localPath, "current", "node_modules")); !os.IsNotExist(err) {
			t.Fatalf("node_modules should be excluded by ignore rule (err=%v)", err)
		}
		if n := len(snapNames(t, localPath)); n != 1 {
			t.Fatalf("want 1 snapshot after first sync, got %d", n)
		}
	})

	t.Run("dry_run_makes_no_snapshot", func(t *testing.T) {
		before := len(snapNames(t, localPath))
		res := gsync(t, work, append([]string{"sync", "--dry-run"}, cfgArg...)...)
		if res.code != 0 {
			t.Fatalf("dry-run code=%d stderr=%q", res.code, res.stderr)
		}
		if after := len(snapNames(t, localPath)); after != before {
			t.Fatalf("dry-run changed snapshot count %d -> %d", before, after)
		}
	})

	t.Run("incremental_and_hardlink_dedup", func(t *testing.T) {
		// Mutate a.txt, add c.txt; b.txt stays byte-identical.
		r.ssh(t, fmt.Sprintf("printf 'hello\\nchanged\\n' > %[1]s/a.txt && printf 'new\\n' > %[1]s/c.txt", remotePath))
		res := gsync(t, work, append([]string{"sync"}, cfgArg...)...)
		if res.code != 0 {
			t.Fatalf("second sync code=%d stderr=%q", res.code, res.stderr)
		}
		names := snapNames(t, localPath)
		if len(names) != 2 {
			t.Fatalf("want 2 snapshots, got %d: %v", len(names), names)
		}
		// The unchanged file must be hardlinked between the two snapshots
		// (same inode), proving the incremental backend shares storage.
		i0 := inode(t, filepath.Join(localPath, "snapshots", names[0], "sub", "b.txt"))
		i1 := inode(t, filepath.Join(localPath, "snapshots", names[1], "sub", "b.txt"))
		if i0 != i1 {
			t.Fatalf("unchanged b.txt not deduped: inodes %d != %d", i0, i1)
		}
		// The changed file must NOT share an inode.
		a0 := inode(t, filepath.Join(localPath, "snapshots", names[0], "a.txt"))
		a1 := inode(t, filepath.Join(localPath, "snapshots", names[1], "a.txt"))
		if a0 == a1 {
			t.Fatalf("changed a.txt wrongly shares inode %d", a0)
		}
	})

	t.Run("snapshots_lists_all", func(t *testing.T) {
		res := gsync(t, work, append([]string{"snapshots", "--name", "web"}, cfgArg...)...)
		lines := nonEmptyLines(res.stdout)
		if res.code != 0 || len(lines) != 2 {
			t.Fatalf("snapshots code=%d lines=%v", res.code, lines)
		}
	})

	t.Run("status_json", func(t *testing.T) {
		res := gsync(t, work, append([]string{"status", "--json"}, cfgArg...)...)
		if res.code != 0 {
			t.Fatalf("status code=%d stderr=%q", res.code, res.stderr)
		}
		var sts []entryStatus
		if err := json.Unmarshal([]byte(res.stdout), &sts); err != nil {
			t.Fatalf("status json: %v\n%s", err, res.stdout)
		}
		if len(sts) != 1 {
			t.Fatalf("want 1 status, got %d", len(sts))
		}
		st := sts[0]
		if st.Name != "web" || st.Count != 2 || st.Backend != "hardlink" {
			t.Fatalf("status = %+v", st)
		}
		// A just-taken snapshot must have a small, non-negative age. This guards
		// the local/UTC parse fix in snapshot.List: a UTC parse skews age by the
		// host's timezone offset (e.g. -8h) and breaks --stale-hours.
		if st.AgeHours < -0.05 || st.AgeHours > 1 {
			t.Fatalf("age_hours = %v, want ~0 (0..1h); a large/negative value means a tz parse bug", st.AgeHours)
		}
		if st.Stale {
			t.Fatalf("fresh backup wrongly marked stale")
		}
	})

	t.Run("restore_latest", func(t *testing.T) {
		dst := filepath.Join(work, "restored")
		res := gsync(t, work, append([]string{"restore", "--name", "web", "--latest", "--to", dst}, cfgArg...)...)
		if res.code != 0 {
			t.Fatalf("restore code=%d stderr=%q", res.code, res.stderr)
		}
		// The latest snapshot reflects the incremental change.
		if b, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(b) != "hello\nchanged\n" {
			t.Fatalf("restored a.txt = %q, err=%v", b, err)
		}
		if _, err := os.Stat(filepath.Join(dst, "c.txt")); err != nil {
			t.Fatalf("restored c.txt missing: %v", err)
		}
	})

	t.Run("prune_deletes_by_retention", func(t *testing.T) {
		if got := len(snapNames(t, localPath)); got != 2 {
			t.Fatalf("precondition: want 2 snapshots, got %d", got)
		}
		// Re-point at a config with recent=1 so prune must drop the older snapshot.
		tightDir := filepath.Join(work, "tight")
		if err := os.MkdirAll(tightDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tightCfg := writeConfig(t, tightDir, r, remotePath, localPath, 1)
		res := gsync(t, work, "prune", "--name", "web", "--config", tightCfg)
		if res.code != 0 {
			t.Fatalf("prune code=%d stderr=%q", res.code, res.stderr)
		}
		if got := len(snapNames(t, localPath)); got != 1 {
			t.Fatalf("after prune recent=1: want 1 snapshot, got %d", got)
		}
	})
}

// TestE2ERemoteRsyncMissing verifies the failure path: syncing from a host with
// no rsync must fail the entry and exit non-zero (so cron/monitors alarm),
// rather than silently succeed or hang. Skips when no such host is configured.
func TestE2ERemoteRsyncMissing(t *testing.T) {
	cfg := loadE2EConfig(t)
	if len(cfg.NoRsyncServer) == 0 {
		t.Skip("no [[no_rsync_server]] in e2e config; skipping remote-rsync-missing test")
	}
	for _, srv := range cfg.NoRsyncServer {
		r := srv.remote()
		t.Run(r.label(), func(t *testing.T) {
			if !r.reachable() {
				t.Skipf("no-rsync server %s@%s:%s unreachable", r.user, r.host, r.port)
			}
			// Sanity-check the premise: this host genuinely lacks rsync. If it grew
			// one, skip rather than assert a failure that can no longer happen.
			if exec.Command("ssh", r.sshArgs("command -v rsync")...).Run() == nil {
				t.Skipf("%s unexpectedly has rsync; skipping remote-missing test", r.host)
			}

			work := t.TempDir()
			localPath := filepath.Join(work, "data", "fail")
			if err := os.MkdirAll(localPath, 0o755); err != nil {
				t.Fatal(err)
			}
			cfgPath := writeConfig(t, work, r, r.remoteBase, localPath, 3)

			res := gsync(t, work, "sync", "--config", cfgPath)
			if res.code == 0 {
				t.Fatalf("sync against rsync-less host should fail, got exit 0\nstdout=%q", res.stdout)
			}
			if !strings.Contains(res.stdout, "失败 1") {
				t.Fatalf("summary should report one failure, stdout=%q", res.stdout)
			}
			// No snapshot may be created when the pull fails.
			if n := len(snapNames(t, localPath)); n != 0 {
				t.Fatalf("failed sync must not snapshot, got %d", n)
			}
		})
	}
}

// --- small helpers ---

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat: no unix inode for %s", path)
	}
	return st.Ino
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// expandHome expands a leading ~ to the user's home directory, so identity paths
// like "~/.ssh/id_ed25519" resolve when handed to ssh via exec (which does no
// shell expansion).
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
