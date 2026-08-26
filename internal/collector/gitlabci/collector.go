// Package gitlabci implements the GitLab CI collector: the pull half of the
// agent, mirroring the field mapping of TrimCI's cloud-pull path so pushed
// and pulled data are indistinguishable server-side.
package gitlabci

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/trimci/agent/internal/api"
	"github.com/trimci/agent/internal/backoff"
	"github.com/trimci/agent/internal/collector"
)

const (
	// maxJobsPerRun mirrors the server-side per-run job bound.
	maxJobsPerRun = 200
	// timeFormat renders query timestamps with sub-second precision — GitLab
	// pipelines carry millisecond updated_at values.
	timeFormat = "2006-01-02T15:04:05.000Z07:00"
)

// terminalStatuses are pipeline states that will never change again — only
// these are worth a detail fetch (mirrors the cloud-pull enrichment gate).
var terminalStatuses = map[string]bool{
	"success": true, "failed": true, "canceled": true, "cancelled": true, "skipped": true,
}

// jobStatuses are the raw pipeline states whose jobs the server stores
// (it accepts jobs only for failed completed runs — failure conclusions are
// failed/canceled in GitLab vocabulary). Fetching jobs for anything else
// would burn the customer's API budget on rows the server drops.
var jobStatuses = map[string]bool{
	"failed": true, "canceled": true, "cancelled": true,
}

// glPipeline is the raw GitLab pipeline shape (LIST summary or detail).
type glPipeline struct {
	ID        int64    `json:"id"`
	IID       *int64   `json:"iid"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Source    string   `json:"source"`
	Ref       string   `json:"ref"`
	WebURL    string   `json:"web_url"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	StartedAt *string  `json:"started_at"`
	Duration  *float64 `json:"duration"`
	User      *glUser  `json:"user"`
}

type glUser struct {
	ID       *int64 `json:"id"`
	Username string `json:"username"`
}

type glJob struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	StartedAt  *string  `json:"started_at"`
	FinishedAt *string  `json:"finished_at"`
	Duration   *float64 `json:"duration"`
}

type glProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

// Collector implements collector.Collector against one GitLab instance.
type Collector struct {
	client  *client
	baseURL string
}

// New builds a GitLab collector. caBundle optionally adds internal CAs.
func New(baseURL, token, caBundle, userAgent string, sleep backoff.Sleeper) (*Collector, error) {
	c, err := newClient(baseURL, token, caBundle, userAgent, sleep)
	if err != nil {
		return nil, err
	}
	return &Collector{client: c, baseURL: baseURL}, nil
}

// Describe probes /api/v4/version. Unreachability is a report, not an error.
func (c *Collector) Describe(ctx context.Context) api.CollectorStatus {
	status := api.CollectorStatus{Kind: "gitlab_ci", BaseURL: c.baseURL, Reachable: true}
	var payload struct {
		Version string `json:"version"`
	}
	if err := c.client.getJSON(ctx, "/api/v4/version", nil, &payload); err != nil {
		status.Reachable = false
		status.Error = truncate(err.Error(), 200)
		return status
	}
	status.Version = payload.Version
	return status
}

// ListProjects lists projects the token can reach, capped at max.
// membership=true keeps a gitlab.com token from enumerating the open
// internet; simple=true trims payloads to the id/path we need.
func (c *Collector) ListProjects(ctx context.Context, max int) ([]api.Project, bool, error) {
	params := url.Values{}
	params.Set("membership", "true")
	params.Set("simple", "true")
	params.Set("order_by", "path")
	params.Set("sort", "asc")

	var out []api.Project
	for page := 1; ; page++ {
		items, hasNext, err := getPage[glProject](ctx, c.client, "/api/v4/projects", params, page)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if len(out) >= max {
				return out, true, nil
			}
			out = append(out, api.Project{ID: item.ID, PathWithNamespace: item.PathWithNamespace})
		}
		if !hasNext {
			return out, false, nil
		}
	}
}

// ListRuns walks pipelines updated since the given time, ascending, via
// filter-keyset pagination (PROTOCOL.md): each full page re-issues the query
// with updated_after = max(updated_at of the page) − 1 s. Offset pagination
// is used only inside a stuck slice (a full page sharing one updated_at, or
// any page that cannot advance the filter), then keyset resumes. Idempotent
// server upserts absorb the 1 s overlap; ascending order keeps crash
// recovery contiguous — the cursor only ever covers an accepted prefix.
func (c *Collector) ListRuns(ctx context.Context, projectID string, since time.Time, page func(collector.RunsPage) error) error {
	path := fmt.Sprintf("/api/v4/projects/%s/pipelines", url.PathEscape(projectID))
	if err := enforceScope(path); err != nil {
		return err
	}

	floor := since.UTC()
	pageNum := 1
	for {
		params := url.Values{}
		params.Set("updated_after", floor.Format(timeFormat))
		params.Set("order_by", "updated_at")
		params.Set("sort", "asc")

		items, _, err := getPage[glPipeline](ctx, c.client, path, params, pageNum)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			// No rows at all (or the exact tail of a full-page walk): one
			// empty Last page lets the caller send the empty final batch.
			return page(collector.RunsPage{Last: true})
		}

		runs, dropped, minTs, maxTs, err := c.buildRuns(ctx, projectID, items)
		if err != nil {
			return err
		}
		last := len(items) < perPage
		if err := page(collector.RunsPage{Runs: runs, Dropped: dropped, Last: last}); err != nil {
			return err
		}
		if last {
			return nil
		}

		newFloor := maxTs.Add(-time.Second)
		if maxTs.IsZero() || !maxTs.After(minTs) || !newFloor.After(floor) {
			// Same-second slice (or a page that cannot move the filter
			// forward): fall back to offset pagination within this filter.
			pageNum++
			continue
		}
		floor = newFloor
		pageNum = 1
	}
}

// buildRuns converts one raw page into protocol runs: drops rows with
// unusable updated_at, enriches terminal rows lacking a user with the
// single-pipeline detail, and attaches jobs to failed completed rows.
func (c *Collector) buildRuns(
	ctx context.Context, projectID string, items []glPipeline,
) (runs []api.Run, dropped int, minTs, maxTs time.Time, err error) {
	runs = make([]api.Run, 0, len(items))
	for _, item := range items {
		updatedAt, parseErr := time.Parse(time.RFC3339, item.UpdatedAt)
		if parseErr != nil {
			// The drop-unparseable-updated_at rule: the server would reject
			// the row anyway, and it must never be resent expecting cursor
			// progress. Dropping is deterministic and reported via hello.
			dropped++
			continue
		}
		if minTs.IsZero() || updatedAt.Before(minTs) {
			minTs = updatedAt
		}
		if updatedAt.After(maxTs) {
			maxTs = updatedAt
		}

		// Cloud-pull enrichment-gate parity: the LIST summary omits user /
		// duration / started_at (and name on older instances). Fetch the
		// detail only for terminal pipelines that lack a user — in-progress
		// rows get re-listed (and enriched) once they settle.
		if terminalStatuses[item.Status] && item.User == nil {
			detail, detailErr := c.getPipeline(ctx, projectID, item.ID)
			switch {
			case detailErr == nil:
				// Keep the LIST's updated_at for cursor math consistency —
				// the detail can be a hair newer than the listed row.
				detail.UpdatedAt = item.UpdatedAt
				item = detail
			case isNotFound(detailErr):
				// Deleted between listing and fetching — ship the summary.
			default:
				return nil, 0, time.Time{}, time.Time{}, detailErr
			}
		}

		run := api.Run{
			ID:        item.ID,
			IID:       item.IID,
			Name:      item.Name,
			Status:    item.Status,
			Source:    item.Source,
			Ref:       item.Ref,
			WebURL:    item.WebURL,
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt,
			StartedAt: item.StartedAt,
			Duration:  item.Duration,
		}
		if item.User != nil {
			run.User = &api.RunUser{ID: item.User.ID, Username: item.User.Username}
		}
		if jobStatuses[item.Status] {
			jobs, jobsErr := c.listJobs(ctx, projectID, item.ID)
			if jobsErr != nil {
				return nil, 0, time.Time{}, time.Time{}, jobsErr
			}
			run.Jobs = jobs
		}
		runs = append(runs, run)
	}
	return runs, dropped, minTs, maxTs, nil
}

func (c *Collector) getPipeline(ctx context.Context, projectID string, pipelineID int64) (glPipeline, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/pipelines/%d", url.PathEscape(projectID), pipelineID)
	var detail glPipeline
	if err := c.client.getJSON(ctx, path, nil, &detail); err != nil {
		return glPipeline{}, err
	}
	return detail, nil
}

func (c *Collector) listJobs(ctx context.Context, projectID string, pipelineID int64) ([]api.RunJob, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/pipelines/%d/jobs", url.PathEscape(projectID), pipelineID)
	var out []api.RunJob
	for page := 1; ; page++ {
		items, hasNext, err := getPage[glJob](ctx, c.client, path, nil, page)
		if err != nil {
			return nil, err
		}
		for _, job := range items {
			if len(out) >= maxJobsPerRun {
				return out, nil
			}
			out = append(out, api.RunJob{
				ID:         job.ID,
				Name:       job.Name,
				Status:     job.Status,
				StartedAt:  job.StartedAt,
				FinishedAt: job.FinishedAt,
				Duration:   job.Duration,
			})
		}
		if !hasNext {
			return out, nil
		}
	}
}

// LogTail fetches the last maxChars characters of a job's trace.
// 404 means the trace was purged — unavailable, a terminal answer.
func (c *Collector) LogTail(ctx context.Context, projectID, jobID string, maxChars int) (string, bool, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/jobs/%s/trace", url.PathEscape(projectID), url.PathEscape(jobID))
	resp, err := c.client.do(ctx, c.client.trace, path, nil)
	if err != nil {
		if isNotFound(err) {
			return "", true, nil
		}
		return "", false, err
	}
	defer resp.Body.Close()
	tail, err := readTail(resp.Body, maxChars)
	if err != nil {
		return "", false, err
	}
	return tail, false, nil
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
