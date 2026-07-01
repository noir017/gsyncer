// Package notify delivers run-completion notifications so an unattended (cron)
// gsync run that fails is not discovered only when a restore is needed. It
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
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/syncer"
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
	// a command like `echo "$GSYNC_SUMMARY" | mail -s gsync admin@x` works without
	// parsing JSON. execx has no stdin, so the JSON travels via the environment.
	env := []string{
		"GSYNC_STATUS=" + p.Status,
		"GSYNC_OK=" + strconv.Itoa(p.OK),
		"GSYNC_FAILED=" + strconv.Itoa(p.Failed),
		"GSYNC_SKIPPED=" + strconv.Itoa(p.Skipped),
		"GSYNC_SUMMARY=" + fmt.Sprintf("gsync %s: ok %d, failed %d, skipped %d, %.1fs",
			p.Status, p.OK, p.Failed, p.Skipped, p.DurationSec),
		"GSYNC_JSON=" + string(body),
	}
	_, err := runner.RunEnv(ctx, env, "sh", "-c", command)
	return err
}
