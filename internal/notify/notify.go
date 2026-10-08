// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

// Package notify delivers run-completion notifications so an unattended (cron)
// gsyncer run that fails is not discovered only when a restore is needed. It
// supports two independent sinks, either or both of which may be configured: an
// HTTP webhook (POST of a JSON body) and a shell command (run via `sh -c` with
// run metadata exposed as GSYNC_* environment variables).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/syncer"
)

// EntryResult is the per-entry portion of a notification payload.
type EntryResult struct {
	Name        string  `json:"name"`
	Host        string  `json:"host"`
	OK          bool    `json:"ok"`
	Skipped     bool    `json:"skipped"`
	Error       string  `json:"error,omitempty"`
	Files       int64   `json:"files"`
	Bytes       int64   `json:"bytes"`
	DurationSec float64 `json:"duration_sec"`
}

// Payload is the JSON body posted to a webhook and serialized into GSYNC_JSON.
type Payload struct {
	Status      string        `json:"status"` // "success" or "failure"
	DryRun      bool          `json:"dry_run"`
	OK          int           `json:"ok"`
	Failed      int           `json:"failed"`
	Skipped     int           `json:"skipped"`
	DurationSec float64       `json:"duration_sec"`
	Entries     []EntryResult `json:"entries"`
}

// Build assembles a Payload from the run results. entries supplies the host for
// each result (results carry only the entry name); dur is the whole-run time.
func Build(results []syncer.Result, entries []config.Sync, dur time.Duration) Payload {
	hostOf := make(map[string]string, len(entries))
	for _, e := range entries {
		hostOf[e.Name] = e.Host
	}
	p := Payload{DurationSec: dur.Seconds()}
	for _, r := range results {
		er := EntryResult{
			Name:        r.Name,
			Host:        hostOf[r.Name],
			OK:          r.OK,
			Skipped:     r.Skipped,
			Files:       r.Files,
			Bytes:       r.Bytes,
			DurationSec: r.Duration.Seconds(),
		}
		if r.Err != nil {
			er.Error = r.Err.Error()
		}
		switch {
		case r.OK:
			p.OK++
		case r.Skipped:
			p.Skipped++
		default:
			p.Failed++
		}
		p.Entries = append(p.Entries, er)
	}
	// A skipped-only run (e.g. another sync held the lock) is not a failure.
	if p.Failed > 0 {
		p.Status = "failure"
	} else {
		p.Status = "success"
	}
	return p
}

// maxErrRunes caps each entry's error in Text: rsync/ssh errors can carry a
// whole stderr tail, and a chat message only needs enough to know where to look.
const maxErrRunes = 300

// Text renders the payload for a human (exported to commands as GSYNC_TEXT):
// a headline, then one line per entry — failures first, so the entry that needs
// attention is not buried in a long weekly batch. It is plain text with no
// markup, ready to pipe into a chat push or a mail body as-is.
func Text(p Payload) string {
	var b strings.Builder
	head := "备份成功"
	if p.Status == "failure" {
		head = "备份失败"
	}
	if p.DryRun {
		head += "（预演）"
	}
	fmt.Fprintf(&b, "%s：成功 %d / 失败 %d", head, p.OK, p.Failed)
	if p.Skipped > 0 {
		fmt.Fprintf(&b, " / 跳过 %d", p.Skipped)
	}
	fmt.Fprintf(&b, " / 耗时 %s", humanDuration(p.DurationSec))
	for _, e := range p.Entries {
		if !e.OK && !e.Skipped {
			fmt.Fprintf(&b, "\n✗ %s（%s）%s", e.Name, e.Host, oneLine(e.Error, maxErrRunes))
		}
	}
	for _, e := range p.Entries {
		if e.Skipped {
			fmt.Fprintf(&b, "\n- %s（%s）跳过", e.Name, e.Host)
			if e.Error != "" {
				b.WriteString("：" + oneLine(e.Error, maxErrRunes))
			}
		}
	}
	for _, e := range p.Entries {
		if e.OK {
			fmt.Fprintf(&b, "\n✓ %s（%s）传输 %d 个文件 / %s / %s",
				e.Name, e.Host, e.Files, humanSize(e.Bytes), humanDuration(e.DurationSec))
		}
	}
	return b.String()
}

// oneLine folds s onto a single line and truncates it to at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// humanSize formats a byte count as a short human string.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// humanDuration rounds to what a reader cares about: tenths under a minute,
// whole seconds above.
func humanDuration(sec float64) string {
	d := time.Duration(sec * float64(time.Second))
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// ShouldSend reports whether the configured switches call for a notification
// given the run's outcome.
func ShouldSend(cfg config.NotifyConfig, p Payload) bool {
	if p.Status == "failure" {
		return cfg.OnFailure
	}
	return cfg.OnSuccess
}

// sinkTimeout bounds each notification sink independently.
const sinkTimeout = 10 * time.Second

// Send delivers the notification to whichever sinks are configured, when the
// switches call for it. Each sink gets its OWN timeout derived from ctx, so a
// slow webhook cannot starve the command sink (they are redundant channels).
// It attempts every configured sink and joins their errors; a nil client
// defaults to a 10s-timeout HTTP client. Notification failures are non-fatal to
// the caller — they should be logged, not allowed to change the run's exit code.
func Send(ctx context.Context, cfg config.NotifyConfig, p Payload, client *http.Client, runner execx.Runner) error {
	if !ShouldSend(cfg, p) {
		return nil
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var errs []error
	if cfg.Webhook != "" {
		wctx, cancel := context.WithTimeout(ctx, sinkTimeout)
		if err := postWebhook(wctx, cfg.Webhook, body, client); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		}
		cancel()
	}
	if cfg.Command != "" {
		cctx, cancel := context.WithTimeout(ctx, sinkTimeout)
		if err := runCommand(cctx, cfg.Command, p, body, runner); err != nil {
			errs = append(errs, fmt.Errorf("command: %w", err))
		}
		cancel()
	}
	return errors.Join(errs...)
}

func postWebhook(ctx context.Context, url string, body []byte, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func runCommand(ctx context.Context, command string, p Payload, body []byte, runner execx.Runner) error {
	// Expose both a machine-readable blob (GSYNC_JSON) and convenient scalars so
	// a command like `echo "$GSYNC_SUMMARY" | mail -s gsyncer admin@x` works without
	// parsing JSON — and GSYNC_TEXT, the per-entry report, so a chat push can say
	// which host failed and why without jq. execx has no stdin, so the JSON
	// travels via the environment.
	env := []string{
		"GSYNC_STATUS=" + p.Status,
		"GSYNC_OK=" + strconv.Itoa(p.OK),
		"GSYNC_FAILED=" + strconv.Itoa(p.Failed),
		"GSYNC_SKIPPED=" + strconv.Itoa(p.Skipped),
		"GSYNC_SUMMARY=" + fmt.Sprintf("gsyncer %s: ok %d, failed %d, skipped %d, %.1fs",
			p.Status, p.OK, p.Failed, p.Skipped, p.DurationSec),
		"GSYNC_TEXT=" + Text(p),
		"GSYNC_JSON=" + string(body),
	}
	_, err := runner.RunEnv(ctx, env, "sh", "-c", command)
	return err
}
