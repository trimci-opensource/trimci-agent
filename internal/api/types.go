// Protocol types for /ingest/v1 (docs/PROTOCOL.md is the human-readable
// contract; the server's own contract tests live in the TrimCI repo as
// trimci/connectors/tests/test_agent_e2e.py).
//
// Request structs marshal EXACTLY the fields the server's validator knows.
// The server drops-and-counts unknown payload fields, so sending anything
// extra would light up its forward-compat counters for nothing. Response
// structs tolerate unknown fields by construction (encoding/json ignores
// them) — the additive-only evolution rule for protocol v1.
package api

// ProtocolVersion is the ingest protocol major version this agent speaks.
const ProtocolVersion = 1

// ── hello ───────────────────────────────────────────────────────────────────

// AgentInfo describes this agent build in the hello status body.
type AgentInfo struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// CollectorStatus reports the local CI instance's health in the hello body.
type CollectorStatus struct {
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	Version   string `json:"version"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error"`
}

// RepoOutcome is one repo's result from the PREVIOUS cycle, reported in the
// next hello. Status is "ok", "error" or "skipped"; Code is a stable machine
// string (PROTOCOL.md lists the vocabulary); Detail is human-readable and
// capped server-side at 200 chars.
type RepoOutcome struct {
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// HelloRequest is the heartbeat + status body.
type HelloRequest struct {
	Protocol   int             `json:"protocol"`
	InstanceID string          `json:"instance_id"`
	Agent      AgentInfo       `json:"agent"`
	Collector  CollectorStatus `json:"collector"`
	Repos      []RepoOutcome   `json:"repos"`
}

// RepoEntry is one active repository in the hello control plane.
type RepoEntry struct {
	ExternalID           string  `json:"external_id"`
	Name                 string  `json:"name"`
	SyncDue              bool    `json:"sync_due"`
	Cursor               *string `json:"cursor"`
	InitialSyncCompleted bool    `json:"initial_sync_completed"`
}

// WantLog identifies one failed job whose log tail the server still needs.
type WantLog struct {
	RepoExternalID string `json:"repo_external_id"`
	RunExternalID  string `json:"run_external_id"`
	JobExternalID  string `json:"job_external_id"`
}

// Limits carries the server-driven caps the agent must respect.
type Limits struct {
	MaxRunsPerBatch   int `json:"max_runs_per_batch"`
	MaxBodyBytes      int `json:"max_body_bytes"`
	LogTailChars      int `json:"log_tail_chars"`
	MaxLogsPerRequest int `json:"max_logs_per_request"`
}

// ConnectionInfo mirrors the hello response's connection block.
type ConnectionInfo struct {
	Name   string `json:"name"`
	Paused bool   `json:"paused"`
}

// HelloResponse is the entire control plane, in one round trip.
type HelloResponse struct {
	Protocol            int            `json:"protocol"`
	MinAgentVersion     string         `json:"min_agent_version"`
	Connection          ConnectionInfo `json:"connection"`
	PollIntervalSeconds int            `json:"poll_interval_seconds"`
	SyncNow             bool           `json:"sync_now"`
	CatalogRequested    bool           `json:"catalog_requested"`
	BackfillNotBefore   string         `json:"backfill_not_before"`
	Limits              Limits         `json:"limits"`
	Repos               []RepoEntry    `json:"repos"`
	WantLogs            []WantLog      `json:"want_logs"`
}

// ── catalog ─────────────────────────────────────────────────────────────────

// Project is one CI project pushed in the catalog.
type Project struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

// CatalogRequest pushes the project catalog. Truncated tells the server the
// list was cut at its cap, so it must not orphan-flip missing repos.
type CatalogRequest struct {
	Protocol  int             `json:"protocol"`
	Collector CollectorStatus `json:"collector"`
	Projects  []Project       `json:"projects"`
	Truncated bool            `json:"truncated"`
}

// CatalogResponse reports the server-side reconcile outcome.
type CatalogResponse struct {
	Discovered    int `json:"discovered"`
	Renamed       int `json:"renamed"`
	Orphaned      int `json:"orphaned"`
	NameConflicts int `json:"name_conflicts"`
}

// ── runs ────────────────────────────────────────────────────────────────────

// RunUser is the pipeline's triggering user, raw GitLab shape.
type RunUser struct {
	ID       *int64 `json:"id,omitempty"`
	Username string `json:"username,omitempty"`
}

// RunJob is one job of a failed completed pipeline, raw GitLab shape.
type RunJob struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	StartedAt  *string  `json:"started_at,omitempty"`
	FinishedAt *string  `json:"finished_at,omitempty"`
	Duration   *float64 `json:"duration,omitempty"`
}

// Run is one pipeline in the provider's RAW vocabulary — status strings like
// "failed" or "waiting_for_resource" go over the wire untranslated, and the
// server normalizes them (a mapping fix is a deploy, not an agent release).
// The field set matches the server validator's known-keys allowlist exactly;
// there are, verifiably, no fields for file contents, diffs, or CI YAML.
type Run struct {
	ID        int64    `json:"id"`
	IID       *int64   `json:"iid,omitempty"`
	Name      string   `json:"name,omitempty"`
	Status    string   `json:"status"`
	Source    string   `json:"source,omitempty"`
	Ref       string   `json:"ref"`
	WebURL    string   `json:"web_url,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	StartedAt *string  `json:"started_at,omitempty"`
	Duration  *float64 `json:"duration,omitempty"`
	User      *RunUser `json:"user,omitempty"`
	Jobs      []RunJob `json:"jobs,omitempty"`
}

// RunsRequest pushes one batch of runs for one repository. Final marks the
// agent's LAST batch for this repo this cycle — it stamps the server-side
// cadence axis, so a cycle with zero new runs still sends one empty final
// batch (PROTOCOL.md, "the empty final batch").
type RunsRequest struct {
	Protocol       int    `json:"protocol"`
	RepoExternalID string `json:"repo_external_id"`
	Final          bool   `json:"final"`
	Runs           []Run  `json:"runs"`
}

// RejectedRun is one row the server refused, with a stable reason code.
type RejectedRun struct {
	ID    any    `json:"id"`
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
}

// RunsResponse carries the advanced cursor and fresh outstanding log wants.
type RunsResponse struct {
	Accepted int           `json:"accepted"`
	Rejected []RejectedRun `json:"rejected"`
	Cursor   *string       `json:"cursor"`
	WantLogs []WantLog     `json:"want_logs"`
}

// ── logs ────────────────────────────────────────────────────────────────────

// LogEntry is one job log tail (or an unavailability report — GitLab purged
// the trace). Unavailable entries still REQUIRE both ids; the server writes
// an empty log row so the job drops out of want_logs permanently.
type LogEntry struct {
	RunExternalID string `json:"run_external_id"`
	JobExternalID string `json:"job_external_id"`
	Tail          string `json:"tail,omitempty"`
	Unavailable   bool   `json:"unavailable,omitempty"`
}

// LogsRequest pushes log tails for one repository.
type LogsRequest struct {
	Protocol       int        `json:"protocol"`
	RepoExternalID string     `json:"repo_external_id"`
	Logs           []LogEntry `json:"logs"`
}

// LogsResponse reports storage counts and the remaining outstanding wants.
type LogsResponse struct {
	Stored   int       `json:"stored"`
	Skipped  int       `json:"skipped"`
	WantLogs []WantLog `json:"want_logs"`
}

// ── error envelope ──────────────────────────────────────────────────────────

// errorEnvelope is the server's uniform error shape:
// {"error": {"code": "...", "message": "..."}}.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Stable error codes from PROTOCOL.md. Codes are the API contract; messages
// are advisory.
const (
	CodeInvalidToken        = "invalid_token"
	CodeTokenRevoked        = "token_revoked"
	CodeConnectionSuspended = "connection_suspended"
	CodeSyncInProgress      = "sync_in_progress"
	CodeUnknownRepo         = "unknown_repo"
	CodeInvalidSchema       = "invalid_schema"
	CodePayloadTooLarge     = "payload_too_large"
	CodeRateLimited         = "rate_limited"
	CodeProtocolUnsupported = "protocol_unsupported"
)
