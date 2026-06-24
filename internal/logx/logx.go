// Package logx provides per-run logging, a summary log, and old-log cleanup.
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const tsLayout = "2006-01-02_150405"

// RunLogger writes one log file for a single run.
type RunLogger struct {
	f *os.File
}

// NewRunLogger creates dir (if needed) and opens dir/<ts>.log.
func NewRunLogger(dir string, ts time.Time) (*RunLogger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, ts.Format(tsLayout)+".log"))
	if err != nil {
		return nil, err
	}
	return &RunLogger{f: f}, nil
}

func (l *RunLogger) write(level, format string, a ...any) {
	fmt.Fprintf(l.f, "%s [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05"), level, fmt.Sprintf(format, a...))
}

// Infof logs at INFO level.
func (l *RunLogger) Infof(format string, a ...any) { l.write("INFO", format, a...) }

// Errorf logs at ERROR level.
func (l *RunLogger) Errorf(format string, a ...any) { l.write("ERROR", format, a...) }

// Close closes the underlying file.
func (l *RunLogger) Close() error { return l.f.Close() }

// AppendSummary appends one line to dir/summary.log.
func AppendSummary(dir, line string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "summary.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

// Cleanup deletes per-run logs beyond keepCount or older than keepDays.
// summary.log and unparseable files are never touched. A zero limit disables
// that rule.
func Cleanup(dir string, keepDays, keepCount int, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type item struct {
		path string
		t    time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "summary.log" || !strings.HasSuffix(name, ".log") {
			continue
		}
		t, err := time.Parse(tsLayout, strings.TrimSuffix(name, ".log"))
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, name), t})
	}
	// newest first
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].t.After(items[i].t) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	for i, it := range items {
		del := false
		if keepCount > 0 && i >= keepCount {
			del = true
		}
		if keepDays > 0 && now.Sub(it.t) > time.Duration(keepDays)*24*time.Hour {
			del = true
		}
		if del {
			_ = os.Remove(it.path)
		}
	}
	return nil
}
