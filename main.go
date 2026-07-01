// Command gsync pulls remote folders over ssh+rsync and keeps GFS snapshots.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/logx"
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
	case "sync":
		os.Exit(cmdSync(os.Args[2:]))
	case "list":
		os.Exit(cmdList(os.Args[2:]))
	case "snapshots":
		os.Exit(cmdSnapshots(os.Args[2:]))
	case "prune":
		os.Exit(cmdPrune(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func loadConfig(cfgFlag string) (*config.Config, error) {
	return config.Load(resolveConfigPath(cfgFlag, exeDir()))
}

func realDeps(log syncer.Logger) syncer.Deps {
	return syncer.Deps{
		Runner: execx.Real{},
		FSType: snapshot.RealFSType,
		Log:    log,
		Now:    time.Now,
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
	results := syncer.SyncMany(ctx, entries, cfg.Defaults, realDeps(rl), *dry)

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
		r := syncer.PruneOne(ctx, s, cfg.Defaults, realDeps(rl))
		fmt.Printf("%s: pruned %d (mode %s)\n", r.Name, r.Pruned, r.Mode)
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
