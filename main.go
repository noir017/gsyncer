// Command gsync pulls remote folders over ssh+rsync and keeps GFS snapshots.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/logx"
	"gsync/internal/notify"
	"gsync/internal/restore"
	"gsync/internal/snapshot"
	"gsync/internal/syncer"
	"gsync/internal/tui"
)

// signalCtx returns a context cancelled on SIGINT/SIGTERM so a cron run can be
// interrupted cleanly (in-flight rsync is killed via CommandContext and no
// further entries are launched).
func signalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

const version = "0.1.0"

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(p)
}

// usage prints the top-level command reference to w.
func usage(w io.Writer) {
	fmt.Fprintf(w, `gsync %s — 通过 ssh+rsync 备份远程文件夹，并保留 GFS 快照

用法:
  gsync                       启动交互式 TUI
  gsync sync [flags]          同步条目 (-name -server -dry-run -config)
  gsync list [-config path]   列出已配置的条目
  gsync snapshots -name N     列出某条目的快照 (-config)
  gsync status [flags]        各条目快照健康度 (-json -stale-hours -config)
  gsync restore -name N       恢复快照到目录 (-at|-latest -to -force -config)
  gsync prune [flags]         按保留策略清理快照 (-name -dry-run -config)
  gsync check [-config path]  只校验配置，不同步
  gsync init [-config -force] 在默认位置写入一份带注释的示例配置
  gsync version               打印版本
  gsync help                  显示本帮助

默认配置路径: %s
`, version, resolveConfigPath("", exeDir()))
}

func main() {
	if len(os.Args) < 2 {
		logDir := filepath.Join(exeDir(), "logs")
		if err := tui.Run(resolveConfigPath("", exeDir()), logDir,
			execx.Real{}, snapshot.RealFSType, time.Now); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("gsync", version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "sync":
		os.Exit(cmdSync(os.Args[2:]))
	case "list":
		os.Exit(cmdList(os.Args[2:]))
	case "snapshots":
		os.Exit(cmdSnapshots(os.Args[2:]))
	case "prune":
		os.Exit(cmdPrune(os.Args[2:]))
	case "restore":
		os.Exit(cmdRestore(os.Args[2:]))
	case "status":
		os.Exit(cmdStatus(os.Args[2:]))
	case "check":
		os.Exit(cmdCheck(os.Args[2:]))
	case "init":
		os.Exit(cmdInit(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

// cmdInit writes a commented starter config to the resolved path, refusing to
// clobber an existing file unless -force is given.
func cmdInit(argv []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path to create")
	force := fs.Bool("force", false, "overwrite an existing config file")
	_ = fs.Parse(argv)

	path := resolveConfigPath(*cfgFlag, exeDir())
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "config already exists at %s (use -force to overwrite)\n", path)
		return 1
	}
	// 0600: the config may reference identity key paths; keep it owner-only,
	// consistent with the tightened perms elsewhere in gsync.
	if err := os.WriteFile(path, []byte(config.StarterTemplate), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	fmt.Printf("wrote starter config to %s\n", path)
	return 0
}

func loadConfig(cfgFlag string) (*config.Config, error) {
	cfg, err := config.Load(resolveConfigPath(cfgFlag, exeDir()))
	if cfg != nil {
		for _, w := range cfg.Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	}
	return cfg, err
}

func realDeps(log syncer.Logger, knownHosts string) syncer.Deps {
	return syncer.Deps{
		Runner:         execx.Real{},
		FSType:         snapshot.RealFSType,
		Log:            log,
		Now:            time.Now,
		KnownHostsFile: knownHosts,
	}
}

func cmdSync(argv []string) int {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	name := fs.String("name", "", "only this entry")
	server := fs.String("server", "", "only entries on this host")
	dry := fs.Bool("dry-run", false, "rsync -n, no snapshot")
	_ = fs.Parse(argv)

	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	logDir := filepath.Join(exeDir(), "logs")
	start := time.Now()
	rl, err := logx.NewRunLogger(logDir, start)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		return 1
	}
	defer rl.Close()

	ctx, stop := signalCtx()
	defer stop()
	entries := selectEntries(cfg.Sync, *name, *server)
	results := syncer.SyncMany(ctx, entries, cfg.Defaults, realDeps(rl, knownHostsPath(*cfgFlag, exeDir())), *dry)

	line := summaryLine(results, time.Since(start))
	rl.Infof("%s", line)
	_ = logx.AppendSummary(logDir, start.Format("2006-01-02 15:04:05")+" "+line)
	_ = logx.Cleanup(logDir, cfg.Log.KeepDays, cfg.Log.KeepCount, time.Now())
	fmt.Println(line)

	// Notify after the run. Use a fresh background context (not the run's, which
	// may be cancelled by ctrl+c — a cancelled run is exactly when a failure
	// alert matters most); Send bounds each sink with its own timeout.
	payload := notify.Build(results, entries, time.Since(start))
	if notify.ShouldSend(cfg.Notify, payload) {
		if err := notify.Send(context.Background(), cfg.Notify, payload, nil, execx.Real{}); err != nil {
			fmt.Fprintln(os.Stderr, "notify:", err)
			rl.Errorf("notify: %v", err)
		}
	}

	for _, r := range results {
		if !r.OK && !r.Skipped {
			return 1
		}
	}
	return 0
}

func cmdStatus(argv []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "output JSON")
	staleHours := fs.Float64("stale-hours", 26,
		"flag entries whose newest snapshot is older than N hours; >0 makes exit 3 when any entry is stale (0 disables)")
	_ = fs.Parse(argv)
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	ctx := context.Background()
	now := time.Now()
	sts := make([]EntryStatus, 0, len(cfg.Sync))
	anyStale := false
	for _, s := range cfg.Sync {
		backend := snapshot.Detect(ctx, s.LocalPath, execx.Real{}, snapshot.RealFSType).Name()
		times, err := snapshot.List(s.LocalPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "status %q: %v\n", s.Name, err)
		}
		st := computeStatus(s.Name, backend, times, now, *staleHours)
		anyStale = anyStale || st.Stale
		sts = append(sts, st)
	}
	if *jsonOut {
		writeStatusJSON(os.Stdout, sts)
	} else {
		writeStatusTable(os.Stdout, sts)
	}
	// Non-zero exit lets a cron/monitor treat "backup too old" as an alarm.
	if *staleHours > 0 && anyStale {
		return 3
	}
	return 0
}

func cmdRestore(argv []string) int {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	name := fs.String("name", "", "entry name (required)")
	at := fs.String("at", "", "snapshot timestamp YYYY-MM-DD_HHMMSS")
	latest := fs.Bool("latest", false, "restore the newest snapshot")
	to := fs.String("to", "", "destination directory (required)")
	force := fs.Bool("force", false, "overwrite destination if it exists")
	_ = fs.Parse(argv)
	if *name == "" || *to == "" {
		fmt.Fprintln(os.Stderr, "restore: --name and --to are required")
		return 2
	}
	// Require exactly one selector: latest xor at.
	if *latest == (*at != "") {
		fmt.Fprintln(os.Stderr, "restore: specify exactly one of --at or --latest")
		return 2
	}
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	entries := selectEntries(cfg.Sync, *name, "")
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "no entry named %q\n", *name)
		return 1
	}
	entry := entries[0]
	times, err := snapshot.List(entry.LocalPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		return 1
	}
	t, err := restore.SelectTime(times, *at, *latest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		// Help the operator pick a valid timestamp when --at missed.
		if len(times) > 0 {
			fmt.Fprintln(os.Stderr, "available snapshots:")
			for _, av := range times {
				fmt.Fprintln(os.Stderr, "  "+av.Format(snapshot.TSLayout))
			}
		}
		return 1
	}
	ctx, stop := signalCtx()
	defer stop()
	snapPath := restore.SnapPath(entry.LocalPath, t)
	if err := restore.Run(ctx, execx.Real{}, entry.LocalPath, snapPath, *to, *force); err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		return 1
	}
	fmt.Printf("restored %s (%s) -> %s\n", entry.Name, t.Format(snapshot.TSLayout), *to)
	return 0
}

func cmdCheck(argv []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	_ = fs.Parse(argv)
	// loadConfig runs Validate and prints any non-fatal warnings to stderr.
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	fmt.Printf("config OK: %d entr%s\n", len(cfg.Sync), plural(len(cfg.Sync)))
	return 0
}

// plural returns "y" for a count of 1 and "ies" otherwise (entr-y / entr-ies).
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func cmdList(argv []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	_ = fs.Parse(argv)
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	for _, s := range cfg.Sync {
		fmt.Printf("%-20s %s@%s:%s -> %s\n", s.Name, s.User, s.Host, s.RemotePath, s.LocalPath)
	}
	return 0
}

func cmdSnapshots(argv []string) int {
	fs := flag.NewFlagSet("snapshots", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	name := fs.String("name", "", "entry name (required)")
	_ = fs.Parse(argv)
	if *name == "" {
		fmt.Fprintln(os.Stderr, "snapshots: --name is required")
		return 2
	}
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	entries := selectEntries(cfg.Sync, *name, "")
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "no entry named %q\n", *name)
		return 1
	}
	times, err := snapshot.List(entries[0].LocalPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		return 1
	}
	for _, t := range times {
		fmt.Println(t.Format(snapshot.TSLayout))
	}
	return 0
}

func cmdPrune(argv []string) int {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	name := fs.String("name", "", "only this entry")
	dry := fs.Bool("dry-run", false, "list snapshots that would be deleted, delete nothing")
	_ = fs.Parse(argv)
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	logDir := filepath.Join(exeDir(), "logs")
	start := time.Now()
	rl, err := logx.NewRunLogger(logDir, start)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		return 1
	}
	defer rl.Close()

	ctx, stop := signalCtx()
	defer stop()
	entries := selectEntries(cfg.Sync, *name, "")
	var results []syncer.Result
	for _, s := range entries {
		if ctx.Err() != nil {
			break
		}
		r := syncer.PruneOne(ctx, s, cfg.Defaults, realDeps(rl, knownHostsPath(*cfgFlag, exeDir())), *dry)
		verb := "pruned"
		if *dry {
			verb = "would prune"
		}
		fmt.Printf("%s: %s %d (mode %s)\n", r.Name, verb, r.Pruned, r.Mode)
		results = append(results, r)
	}

	line := summaryLine(results, time.Since(start))
	rl.Infof("%s", line)
	_ = logx.AppendSummary(logDir, start.Format("2006-01-02 15:04:05")+" "+line)
	_ = logx.Cleanup(logDir, cfg.Log.KeepDays, cfg.Log.KeepCount, time.Now())
	fmt.Println(line)

	for _, r := range results {
		if !r.OK && !r.Skipped {
			return 1
		}
	}
	return 0
}
