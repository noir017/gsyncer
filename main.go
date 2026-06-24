// Command gsync pulls remote folders over ssh+rsync and keeps GFS snapshots.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/logx"
	"gsync/internal/snapshot"
	"gsync/internal/syncer"
)

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
		fmt.Fprintln(os.Stderr, "TUI 尚未实现（见后续计划）。可用子命令: sync | list | snapshots | prune | version")
		os.Exit(2)
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

	entries := selectEntries(cfg.Sync, *name, *server)
	results := syncer.SyncMany(context.Background(), entries, cfg.Defaults, realDeps(rl), *dry)

	line := summaryLine(results, time.Since(start))
	rl.Infof("%s", line)
	_ = logx.AppendSummary(logDir, start.Format("2006-01-02 15:04:05")+" "+line)
	_ = logx.Cleanup(logDir, cfg.Log.KeepDays, cfg.Log.KeepCount, time.Now())
	fmt.Println(line)

	for _, r := range results {
		if !r.OK {
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

	entries := selectEntries(cfg.Sync, *name, "")
	var results []syncer.Result
	for _, s := range entries {
		r := syncer.PruneOne(context.Background(), s, cfg.Defaults, realDeps(rl))
		fmt.Printf("%s: pruned %d (mode %s)\n", r.Name, r.Pruned, r.Mode)
		results = append(results, r)
	}

	line := summaryLine(results, time.Since(start))
	rl.Infof("%s", line)
	_ = logx.AppendSummary(logDir, start.Format("2006-01-02 15:04:05")+" "+line)
	_ = logx.Cleanup(logDir, cfg.Log.KeepDays, cfg.Log.KeepCount, time.Now())
	fmt.Println(line)

	for _, r := range results {
		if !r.OK {
			return 1
		}
	}
	return 0
}
