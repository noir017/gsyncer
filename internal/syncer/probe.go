// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package syncer

import (
	"context"
	"strings"
	"time"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
)

const (
	// probeAttempts / probeRetryDelay: a host counts as offline only after this
	// many failed probes this far apart, so a momentary blip (a Teleport cert
	// rotating mid-handshake, a Wi-Fi reassociation) cannot cost a "skip" entry
	// a whole scheduled run.
	probeAttempts = 2
	// probeTimeout bounds one probe end to end. ssh's ConnectTimeout does not
	// cover a ProxyCommand (e.g. tsh proxy ssh), so the context has to.
	probeTimeout = 30 * time.Second
)

// probeRetryDelay is a var so tests can drop it to zero.
var probeRetryDelay = 30 * time.Second

// ProbeResult is what one reachability check found. Exactly one of Online and
// Offline is set, or neither: the host answered but something else is wrong
// (key rejected, host key changed). That third case is deliberately not
// "offline" — it will not fix itself, so the caller lets the sync run and fail
// with its full error, which alerts, instead of retrying it quietly forever.
type ProbeResult struct {
	Online  bool
	Offline bool
	Detail  string // ssh's stderr, trimmed, for the log and the state file
}

// offlineMarkers are ssh / Teleport messages that mean "could not get to the
// host at all". Collected from real failures on the deployment this was built
// for: a Teleport agent node that is not connected, an agentless node whose
// address does not answer, and plain ssh to dead, refused and unresolvable
// addresses.
var offlineMarkers = []string{
	"is offline or does not exist", // Teleport: agent node not connected to the cluster
	"failed connecting to host",    // Teleport: proxy could not dial the node
	"no tunnel connection found",   // Teleport: agent still listed, reverse tunnel gone
	"connection timed out",
	"operation timed out",
	"no route to host",
	"connection refused",
	"network is unreachable",
	"connection reset",
	"could not resolve hostname",
	"name or service not known",
	"temporary failure in name resolution",
}

// classifyProbe interprets one `ssh ... true` run.
func classifyProbe(out execx.Result, err error, timedOut bool) ProbeResult {
	detail := strings.TrimSpace(out.Stderr)
	switch {
	case err == nil:
		return ProbeResult{Online: true}
	case timedOut:
		return ProbeResult{Offline: true, Detail: "probe timed out after " + probeTimeout.String()}
	case out.Code == 255:
		// 255 is ssh's own failure status; which failure it was is in stderr.
		low := strings.ToLower(detail)
		for _, m := range offlineMarkers {
			if strings.Contains(low, m) {
				return ProbeResult{Offline: true, Detail: detail}
			}
		}
		return ProbeResult{Detail: detail}
	case out.Code > 0:
		// Any other status is the remote command's: we connected and
		// authenticated, and `true` merely failed — e.g. a Windows login shell
		// that has no `true`. The host is up.
		return ProbeResult{Online: true, Detail: detail}
	default:
		// ssh itself could not be started. Not the host's fault.
		msg := detail
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return ProbeResult{Detail: msg}
	}
}

// Probe checks whether the entry's host is reachable, over exactly the path the
// sync would use (same user, port, identity, host-key store — and so the same
// ssh_config ProxyCommand). It tries probeAttempts times before calling the
// host offline.
func Probe(ctx context.Context, s config.Sync, d config.Defaults, deps Deps) ProbeResult {
	var pr ProbeResult
	for i := 0; i < probeAttempts; i++ {
		if i > 0 && !sleepCtx(ctx, probeRetryDelay) {
			break
		}
		pr = probeOnce(ctx, s, d, deps)
		if !pr.Offline {
			return pr
		}
	}
	return pr
}

func probeOnce(ctx context.Context, s config.Sync, d config.Defaults, deps Deps) ProbeResult {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := append(sshBaseArgs(s.Identity, s.EffectivePort(d), s.StrictHostKey, deps.KnownHostsFile),
		s.User+"@"+s.Host, "true")
	out, err := deps.Runner.Run(pctx, "ssh", args...)
	return classifyProbe(out, err, err != nil && pctx.Err() != nil && ctx.Err() == nil)
}

// sleepCtx waits d or until ctx is done; it reports whether the full wait
// elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
