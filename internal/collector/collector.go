// Package collector defines the provider-side interface of the agent.
//
// A Collector pulls pipeline observability data from a CI instance inside
// the customer network. GitLab CI is the only implementation today; the
// interface (and the raw-payload protocol it feeds) is the seam where a
// Jenkins collector plugs in later without protocol changes.
//
// Note for spec readers: the design sketch listed a separate ListJobs
// method. Jobs are attached inline by ListRuns instead — the push protocol
// carries them inline on failed runs, so a separate call had no caller.
package collector

import (
	"context"
	"time"

	"github.com/trimci-opensource/trimci-agent/internal/api"
)

// RunsPage is one ascending page of runs handed to the ListRuns callback.
type RunsPage struct {
	// Runs in the provider's raw vocabulary, ascending by updated_at.
	Runs []api.Run
	// Dropped counts rows discarded agent-side because their updated_at was
	// missing or unparseable (the drop-unparseable-updated_at rule): the
	// server would reject them anyway, and they must never be resent
	// expecting cursor progress.
	Dropped int
	// Last marks the final page of this walk. A walk with no runs at all
	// still delivers one empty Last page, so the caller can send the empty
	// final batch that stamps the server-side cadence.
	Last bool
}

// Collector pulls observability data from one CI instance.
type Collector interface {
	// Describe probes the instance and reports its health for the hello
	// status body. It never returns an error — unreachability IS the report.
	Describe(ctx context.Context) api.CollectorStatus

	// ListProjects returns up to max projects visible to the credentials,
	// with truncated=true when more exist beyond the cap.
	ListProjects(ctx context.Context, max int) (projects []api.Project, truncated bool, err error)

	// ListRuns walks runs updated since the given time, ascending by
	// updated_at, calling page for each fetched page (see PROTOCOL.md for
	// the filter-keyset pull protocol). Runs are enriched and carry jobs
	// where the protocol wants them. A non-nil error from page stops the
	// walk and is returned as-is.
	ListRuns(ctx context.Context, projectID string, since time.Time, page func(RunsPage) error) error

	// LogTail returns the last maxChars characters of a job's log.
	// unavailable=true means the provider no longer has the trace (purged) —
	// a terminal answer, not an error.
	LogTail(ctx context.Context, projectID, jobID string, maxChars int) (tail string, unavailable bool, err error)
}
