package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/trimci-opensource/trimci-agent/internal/api"
	"github.com/trimci-opensource/trimci-agent/internal/backoff"
	"github.com/trimci-opensource/trimci-agent/internal/collector"
)

// errRepoGone means the server no longer owns/actives this repo (404
// unknown_repo) — e.g. deactivated in the dashboard mid-cycle. Not an error;
// the next hello simply won't list it.
var errRepoGone = errors.New("repository is no longer active on this connection")

// errRateWindow means a 429 whose Retry-After exceeds the inline ceiling —
// the repo is abandoned for this cycle (it stays due server-side) so the
// hello heartbeat keeps running during long rate windows.
type errRateWindow struct{ after time.Duration }

func (e *errRateWindow) Error() string {
	return fmt.Sprintf("server rate window open for %s", e.after)
}

// cycle performs one unit of work under a fresh control plane: catalog if
// requested, then every due repo to exhaustion, then the want_logs drain.
func (a *Agent) cycle(ctx context.Context, ctl *api.HelloResponse) {
	if ctl.CatalogRequested {
		if stop := a.pushCatalog(ctx, ctl); stop {
			return
		}
	}
	wants := ctl.WantLogs
	for _, repo := range ctl.Repos {
		if ctx.Err() != nil {
			return
		}
		if !repo.SyncDue {
			// The cycle rule cuts the other way too: due flags gate STARTING
			// a repo; a started repo runs to exhaustion inside syncRepo
			// regardless of what interleaved hellos would say.
			continue
		}
		outcome, latestWants, stop := a.syncRepo(ctx, ctl, repo)
		a.recordOutcome(outcome)
		if latestWants != nil {
			wants = latestWants
		}
		if stop {
			return
		}
	}
	a.drainLogs(ctx, ctl, wants)
}

func (a *Agent) recordOutcome(outcome api.RepoOutcome) {
	if len(a.outcomes) < maxReportedOutcomes {
		a.outcomes = append(a.outcomes, outcome)
	}
}

// ── catalog ─────────────────────────────────────────────────────────────────

func (a *Agent) pushCatalog(ctx context.Context, ctl *api.HelloResponse) (stop bool) {
	projects, truncated, err := a.Collector.ListProjects(ctx, catalogMaxProjects)
	if err != nil {
		// catalog_requested stays set server-side (it clears only on a
		// successful reconcile), so this retries automatically next cycle.
		a.Log.Warn("listing projects failed — retrying next cycle", "error", err)
		return false
	}
	resp, err := a.API.PushCatalog(ctx, api.CatalogRequest{
		Collector: a.Collector.Describe(ctx),
		Projects:  projects,
		Truncated: truncated,
	})
	if err != nil {
		if isCycleStopper(err) {
			return true
		}
		a.Log.Warn("catalog push failed — retrying next cycle", "error", err)
		return false
	}
	a.Log.Info("catalog pushed",
		"projects", len(projects), "truncated", truncated, "discovered", resp.Discovered,
		"renamed", resp.Renamed, "orphaned", resp.Orphaned, "name_conflicts", resp.NameConflicts)
	return false
}

// ── runs ────────────────────────────────────────────────────────────────────

// syncRepo drives one repository to exhaustion: walk ascending pages from
// the server-side cursor, push each as one or more batches, mark the last
// one final. Crash-safe by construction — the cursor only ever covers an
// accepted prefix, and a repo that never got its final flag stays due.
func (a *Agent) syncRepo(
	ctx context.Context, ctl *api.HelloResponse, repo api.RepoEntry,
) (api.RepoOutcome, []api.WantLog, bool) {
	outcome := api.RepoOutcome{ExternalID: repo.ExternalID, Status: "ok"}

	since, err := sinceOf(repo, ctl)
	if err != nil {
		outcome.Status = "error"
		outcome.Code = "bad_cursor"
		outcome.Detail = truncateDetail(err.Error())
		return outcome, nil, false
	}

	push := &repoPush{
		agent:    a,
		ctx:      ctx,
		repo:     repo,
		limits:   ctl.Limits,
		batchCap: max(1, ctl.Limits.MaxRunsPerBatch),
	}
	a.Log.Info("syncing repository", "repo", repo.Name, "since", since.Format(time.RFC3339))
	walkErr := a.Collector.ListRuns(ctx, repo.ExternalID, since, push.page)

	switch {
	case walkErr == nil:
		if push.rejected > 0 || push.dropped > 0 || push.poisonSkipped > 0 {
			outcome.Code = "partial"
			outcome.Detail = fmt.Sprintf("%d accepted, %d rejected, %d dropped, %d skipped",
				push.accepted, push.rejected, push.dropped, push.poisonSkipped)
		} else {
			outcome.Detail = fmt.Sprintf("%d runs pushed", push.accepted)
		}
		a.Log.Info("repository synced", "repo", repo.Name, "accepted", push.accepted,
			"rejected", push.rejected, "dropped", push.dropped)
	case errors.Is(walkErr, errRepoGone):
		outcome.Status = "skipped"
		outcome.Code = "unknown_repo"
	case isRateWindow(walkErr):
		outcome.Status = "skipped"
		outcome.Code = "rate_limited"
		outcome.Detail = "server rate window open; resuming next cycle"
		a.Log.Info("rate window open — repo resumes next cycle", "repo", repo.Name)
	case isCycleStopper(walkErr):
		outcome.Status = "error"
		outcome.Code = "aborted"
		outcome.Detail = truncateDetail(walkErr.Error())
		return outcome, push.wants, true
	default:
		outcome.Status = "error"
		if api.Status(walkErr) != 0 {
			outcome.Code = "push_failed"
		} else {
			outcome.Code = "collector_error"
		}
		outcome.Detail = truncateDetail(walkErr.Error())
		a.Log.Warn("repository sync failed — repo stays due and retries automatically",
			"repo", repo.Name, "error", walkErr)
	}
	return outcome, push.wants, false
}

// sinceOf picks the walk's start: the server cursor, else the backfill
// floor (now − retention window) the server sent.
func sinceOf(repo api.RepoEntry, ctl *api.HelloResponse) (time.Time, error) {
	raw := ctl.BackfillNotBefore
	if repo.Cursor != nil && *repo.Cursor != "" {
		raw = *repo.Cursor
	}
	since, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("unusable cursor %q: %w", raw, err)
	}
	return since, nil
}

// repoPush accumulates one repo's push state across pages. It carries the
// cycle ctx because the collector's page callback has no ctx parameter.
type repoPush struct {
	agent    *Agent
	ctx      context.Context
	repo     api.RepoEntry
	limits   api.Limits
	batchCap int

	accepted      int
	rejected      int
	dropped       int
	poisonSkipped int
	wants         []api.WantLog
	retryBackoff  backoff.Backoff
}

// page pushes one collector page as one or more protocol batches, marking
// the very last batch of the walk final.
func (p *repoPush) page(pg collector.RunsPage) error {
	p.dropped += pg.Dropped
	batches := p.splitBatches(pg.Runs)
	if len(batches) == 0 {
		if !pg.Last {
			return nil
		}
		// The empty final batch: a due repo with nothing new must still
		// stamp the server-side cadence axis, or it would be re-walked
		// every cycle forever.
		batches = [][]api.Run{{}}
	}
	for i, batch := range batches {
		final := pg.Last && i == len(batches)-1
		if err := p.pushSlice(p.ctx, batch, final); err != nil {
			return err
		}
	}
	return nil
}

// splitBatches packs runs into batches bounded by the server's batch-size
// cap and body-byte cap (with envelope headroom).
func (p *repoPush) splitBatches(runs []api.Run) [][]api.Run {
	byteLimit := p.limits.MaxBodyBytes - 4096
	if byteLimit < 4096 {
		byteLimit = 4096
	}
	var batches [][]api.Run
	var current []api.Run
	size := 0
	for _, run := range runs {
		encoded, err := json.Marshal(run)
		if err != nil { // cannot happen for these types; drop defensively
			p.dropped++
			continue
		}
		cost := len(encoded) + 1
		if len(current) > 0 && (size+cost > byteLimit || len(current) >= p.batchCap) {
			batches = append(batches, current)
			current = nil
			size = 0
		}
		current = append(current, run)
		size += cost
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// pushSlice delivers one slice, applying the protocol's recovery rules:
// Retry-After honored exactly on 429 (long windows abort the repo instead,
// keeping the heartbeat alive); 413 halves; two consecutive 5xx/transport
// failures of the same slice halve too; a single-run slice that still fails
// is skipped and reported (the agent half of the poison-pill rule).
func (p *repoPush) pushSlice(ctx context.Context, runs []api.Run, final bool) error {
	consecutiveFailures := 0
	mutexRetries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := p.agent.API.PushRuns(ctx, api.RunsRequest{
			RepoExternalID: p.repo.ExternalID,
			Final:          final,
			Runs:           runs,
		})
		if err == nil {
			p.accepted += resp.Accepted
			p.rejected += len(resp.Rejected)
			p.wants = resp.WantLogs
			p.retryBackoff.Reset()
			return nil
		}

		status := api.Status(err)
		switch {
		case isCycleStopper(err):
			return err
		case api.IsCode(err, api.CodeUnknownRepo):
			return errRepoGone
		case status == 429:
			after := api.RetryAfter(err)
			if after <= 0 {
				after = time.Minute
			}
			if after > inlineRetryAfterCeiling {
				return &errRateWindow{after: after}
			}
			if err := p.agent.Sleep(ctx, after); err != nil {
				return err
			}
		case api.IsCode(err, api.CodeSyncInProgress):
			// Another in-flight request for this connection (a stuck slot or
			// a duplicate agent). Short pause, bounded patience.
			mutexRetries++
			if mutexRetries > 3 {
				return err
			}
			if err := p.agent.Sleep(ctx, 30*time.Second); err != nil {
				return err
			}
		case status == 413:
			if len(runs) <= 1 {
				p.skipPoison(runs, err)
				return nil
			}
			p.batchCap = max(1, len(runs)/2) // future pages pre-split
			return p.halve(ctx, runs, final)
		case status == 0 || status >= 500:
			// Transport failure / timeout / server error.
			consecutiveFailures++
			if len(runs) > 1 && consecutiveFailures >= 2 {
				return p.halve(ctx, runs, final)
			}
			if len(runs) <= 1 && consecutiveFailures >= 3 {
				p.skipPoison(runs, err)
				return nil
			}
			if err := p.agent.Sleep(ctx, p.retryBackoff.Next()); err != nil {
				return err
			}
		default:
			// Deterministic 4xx (invalid_schema, …): retrying cannot help.
			return err
		}
	}
}

func (p *repoPush) halve(ctx context.Context, runs []api.Run, final bool) error {
	mid := len(runs) / 2
	if err := p.pushSlice(ctx, runs[:mid], false); err != nil {
		return err
	}
	return p.pushSlice(ctx, runs[mid:], final)
}

func (p *repoPush) skipPoison(runs []api.Run, cause error) {
	p.poisonSkipped += len(runs)
	id := int64(0)
	if len(runs) > 0 {
		id = runs[0].ID
	}
	p.agent.Log.Warn("skipping one undeliverable run this cycle (poison-pill rule)",
		"repo", p.repo.Name, "run_id", id, "cause", cause)
}

// ── logs ────────────────────────────────────────────────────────────────────

// drainLogs works the outstanding want_logs list. The list is DB-state
// server-side (recomputed on every hello and runs response), so anything
// left over — crash, cap, rate window — simply drains on later cycles.
func (a *Agent) drainLogs(ctx context.Context, ctl *api.HelloResponse, wants []api.WantLog) {
	maxPerRequest := ctl.Limits.MaxLogsPerRequest
	if maxPerRequest <= 0 {
		maxPerRequest = 20
	}
	tailChars := ctl.Limits.LogTailChars
	if tailChars <= 0 {
		tailChars = 8000
	}

	requests := 0
	for len(wants) > 0 && requests < maxLogRequestsPerCycle {
		if ctx.Err() != nil {
			return
		}
		repoID := wants[0].RepoExternalID
		group := make([]api.WantLog, 0, maxPerRequest)
		for _, want := range wants {
			if want.RepoExternalID == repoID && len(group) < maxPerRequest {
				group = append(group, want)
			}
		}

		entries := make([]api.LogEntry, 0, len(group))
		for _, want := range group {
			tail, unavailable, err := a.Collector.LogTail(ctx, repoID, want.JobExternalID, tailChars)
			if err != nil {
				a.Log.Warn("fetching a job trace failed — log push resumes next cycle",
					"repo", repoID, "job", want.JobExternalID, "error", err)
				return
			}
			entry := api.LogEntry{RunExternalID: want.RunExternalID, JobExternalID: want.JobExternalID}
			if unavailable {
				entry.Unavailable = true
			} else {
				entry.Tail = tail
			}
			entries = append(entries, entry)
		}

		resp, err := a.API.PushLogs(ctx, api.LogsRequest{RepoExternalID: repoID, Logs: entries})
		requests++
		if err != nil {
			switch {
			case api.IsCode(err, api.CodeUnknownRepo):
				wants = withoutRepo(wants, repoID)
				continue
			case api.Status(err) == 429:
				after := api.RetryAfter(err)
				if after > 0 && after <= inlineRetryAfterCeiling {
					if a.Sleep(ctx, after) != nil {
						return
					}
					continue
				}
				a.Log.Info("log push rate limited — resuming next cycle")
				return
			case isCycleStopper(err):
				return
			default:
				a.Log.Warn("log push failed — resuming next cycle", "error", err)
				return
			}
		}

		previous := wantsKey(wants)
		wants = resp.WantLogs
		if resp.Stored == 0 && wantsKey(wants) == previous {
			// No progress and an identical outstanding set: bail out rather
			// than livelock on entries the server keeps skipping.
			a.Log.Warn("log drain made no progress — deferring to next cycle", "outstanding", len(wants))
			return
		}
	}
}

func withoutRepo(wants []api.WantLog, repoID string) []api.WantLog {
	out := wants[:0]
	for _, want := range wants {
		if want.RepoExternalID != repoID {
			out = append(out, want)
		}
	}
	return out
}

func wantsKey(wants []api.WantLog) string {
	var b strings.Builder
	for _, want := range wants {
		b.WriteString(want.RepoExternalID)
		b.WriteByte(':')
		b.WriteString(want.JobExternalID)
		b.WriteByte(',')
	}
	return b.String()
}

// isCycleStopper: errors after which no further data push this cycle can
// succeed — suspension (pause landed mid-cycle) and auth failures. The next
// hello re-establishes the state of the world.
func isCycleStopper(err error) bool {
	return api.IsCode(err, api.CodeConnectionSuspended) ||
		api.Status(err) == 401 ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func isRateWindow(err error) bool {
	var rateErr *errRateWindow
	return errors.As(err, &rateErr)
}

func truncateDetail(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200]
}
