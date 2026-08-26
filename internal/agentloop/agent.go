// Package agentloop is the agent's brain: the hello-driven control loop.
//
// One process, one loop, sequential per repo. The agent keeps NO local
// state — every cycle asks the server what to do (which repos, from which
// cursors), so crash/restart/reinstall recovery is "send hello", and an
// accidentally duplicated agent degrades to wasted work, never corruption.
package agentloop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/trimci/agent/internal/api"
	"github.com/trimci/agent/internal/backoff"
	"github.com/trimci/agent/internal/collector"
	"github.com/trimci/agent/internal/healthz"
	"github.com/trimci/agent/internal/semver"
	"github.com/trimci/agent/internal/version"
)

const (
	// maxReportedOutcomes mirrors the server's heartbeat sanitizer cap.
	maxReportedOutcomes = 50
	// catalogMaxProjects mirrors the server-side catalog cap; listing more
	// would only be truncated server-side anyway.
	catalogMaxProjects = 2000
	// maxLogRequestsPerCycle bounds the want_logs drain per cycle. The
	// backlog self-heals: the server recomputes want_logs from its own
	// state on every hello, so whatever is left drains next cycle.
	maxLogRequestsPerCycle = 30
	// inlineRetryAfterCeiling: a 429 whose Retry-After fits within roughly
	// one poll interval is honored inline (sleep + retry, the cycle rule).
	// Longer windows (e.g. daily free-plan limits) abort the repo instead,
	// so the hello heartbeat keeps running and the repo — still lacking its
	// final flag — stays due and resumes when the window opens.
	inlineRetryAfterCeiling = 90 * time.Second
	// tokenRetryDelay paces hello retries while the token is rejected: slow
	// enough to be polite, fast enough that a freshly minted token recovers
	// the agent within minutes, without a restart.
	tokenRetryDelay = 5 * time.Minute
	// protocolRetryDelay paces hello while the server says this protocol
	// version is retired (426): keep polling slowly — hello stays
	// shape-compatible across versions by contract.
	protocolRetryDelay  = 15 * time.Minute
	defaultPollInterval = 60 * time.Second
)

// Agent runs the control loop against one TrimCI connection and one CI
// instance.
type Agent struct {
	API       *api.Client
	Collector collector.Collector
	Log       *slog.Logger
	Sleep     backoff.Sleeper
	Health    *healthz.State // optional
	// DashboardURL is only used in operator-facing log messages.
	DashboardURL string
	// Once makes Run perform a single cycle and return (cron mode).
	Once bool

	instanceID string
	outcomes   []api.RepoOutcome
}

// Run drives the loop until ctx is canceled (or one cycle in Once mode).
func (a *Agent) Run(ctx context.Context) error {
	if a.Sleep == nil {
		a.Sleep = backoff.Sleep
	}
	a.instanceID = newInstanceID()
	a.Log.Info("agent starting",
		"version", version.Version, "os", runtime.GOOS, "arch", runtime.GOARCH, "instance_id", a.instanceID)

	helloBackoff := &backoff.Backoff{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ctl, err := a.API.Hello(ctx, a.buildHello(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			delay := a.classifyHelloFailure(err, helloBackoff)
			if a.Once {
				return fmt.Errorf("hello failed: %w", err)
			}
			if err := a.Sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		helloBackoff.Reset()
		interval := pollInterval(ctl)
		if a.Health != nil {
			a.Health.HelloSucceeded(interval)
		}
		a.outcomes = nil // delivered with this hello

		if ctl.MinAgentVersion != "" && semver.Compare(version.Version, ctl.MinAgentVersion) < 0 {
			a.Log.Warn("this agent version is below the server's supported floor — please upgrade",
				"version", version.Version, "min_agent_version", ctl.MinAgentVersion)
		}

		if ctl.Connection.Paused {
			a.Log.Info("connection is paused in the TrimCI dashboard — standing by", "connection", ctl.Connection.Name)
		} else {
			a.cycle(ctx, ctl)
		}

		if a.Once {
			return nil
		}
		if err := a.Sleep(ctx, interval); err != nil {
			return err
		}
	}
}

// buildHello assembles the status body: agent build info, collector health
// probed fresh, and the per-repo outcomes of the previous cycle.
func (a *Agent) buildHello(ctx context.Context) api.HelloRequest {
	outcomes := a.outcomes
	if len(outcomes) > maxReportedOutcomes {
		outcomes = outcomes[:maxReportedOutcomes]
	}
	if outcomes == nil {
		outcomes = []api.RepoOutcome{}
	}
	return api.HelloRequest{
		InstanceID: a.instanceID,
		Agent:      api.AgentInfo{Version: version.Version, OS: runtime.GOOS, Arch: runtime.GOARCH},
		Collector:  a.Collector.Describe(ctx),
		Repos:      outcomes,
	}
}

// classifyHelloFailure logs the failure in operator terms and returns the
// retry delay. The agent never exits on server-side trouble: a revoked
// token, a retired protocol or an outage all keep polling and recover
// without a restart.
func (a *Agent) classifyHelloFailure(err error, b *backoff.Backoff) time.Duration {
	switch {
	case api.IsCode(err, api.CodeTokenRevoked) || api.IsCode(err, api.CodeInvalidToken) || api.Status(err) == 401:
		a.Log.Error("TrimCI rejected the agent token — rotate or re-issue it in the dashboard "+
			"(Integration health); the agent will keep retrying and recovers without a restart",
			"dashboard", a.DashboardURL, "error", err)
		return tokenRetryDelay
	case api.Status(err) == 426:
		a.Log.Error("the server no longer supports this agent's protocol version — upgrade the agent",
			"version", version.Version, "error", err)
		return protocolRetryDelay
	case api.Status(err) == 429:
		delay := api.RetryAfter(err)
		if delay <= 0 {
			delay = time.Minute
		}
		a.Log.Warn("hello rate limited", "retry_after", delay)
		return delay
	default:
		delay := b.Next()
		a.Log.Warn("hello failed — backing off", "error", err, "retry_in", delay)
		return delay
	}
}

func pollInterval(ctl *api.HelloResponse) time.Duration {
	if ctl.PollIntervalSeconds > 0 {
		return time.Duration(ctl.PollIntervalSeconds) * time.Second
	}
	return defaultPollInterval
}

// newInstanceID builds a per-process id (hostname + random suffix, ≤ 64
// chars server-side) so the dashboard can detect two agents sharing one
// token. Random per process on purpose: identity is "this running process",
// not "this host".
func newInstanceID() string {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "agent"
	}
	if len(host) > 50 {
		host = host[:50]
	}
	return host + "-" + hex.EncodeToString(suffix)
}
