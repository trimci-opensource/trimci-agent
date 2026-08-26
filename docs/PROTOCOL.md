# TrimCI Agent protocol v1

The wire contract between a push-mode agent and TrimCI's ingest API
(`https://trimci.com/ingest/v1/`). This document is complete enough to implement a
compatible agent from scratch; the reference implementation lives in this repository.

## Transport rules

- **POST only.** Every endpoint accepts only `POST`; other methods are rejected.
- **Trailing slashes are part of the URL.** The endpoints are mounted
  slash-terminated (`/ingest/v1/hello/`). Do not call them without the slash: the
  server's redirect to the canonical URL does not preserve a POST body.
- Bodies are JSON objects (`Content-Type: application/json`). Non-finite JSON
  constants (`NaN`, `Infinity`) are rejected.
- **Body caps**: 1 MiB per request (2 MiB for `catalog`), checked against
  `Content-Length` before the body is read. Oversize → `413 payload_too_large`.
  The authoritative cap arrives in every hello response (`limits.max_body_bytes`).
- Send `Authorization: Bearer <agent token>` and an
  `X-TrimCI-Agent: trimci-agent/<version>` header on every request.
- TLS is required. (The reference agent refuses `http://` server URLs outside of a
  development escape hatch.)

## Authentication

Agent tokens have the form `trimci_agent_<key_id>_<secret>`:

- `trimci_agent_` — fixed, greppable prefix (register it with your secret scanner);
- `key_id` — 8 base62 chars, a non-secret lookup key, safe in logs;
- `secret` — 256-bit random.

The token is shown exactly once in the TrimCI dashboard; the server stores only a
SHA-256 hash. Rotation issues a second token with a 24-hour grace overlap, so a
rotation is zero-downtime. Auth failures return a constant-shape `401` with code
`invalid_token` or `token_revoked`; repeated failures are throttled per key and per
IP (`429`).

The token **is** the tenant: payloads carry no organization, connection, or
repo-ownership claims. Everything is derived server-side from the token, so
cross-tenant writes are impossible by construction.

## Error envelope

Every non-2xx response carries:

```json
{"error": {"code": "<stable machine string>", "message": "<human-readable, English>"}}
```

Codes are the API contract; messages are advisory and may change.

| Code | Status | Meaning / required agent behavior |
|---|---|---|
| `invalid_token` | 401 | Malformed/unknown token. Log actionably, retry slowly, never exit. |
| `token_revoked` | 401 | Token revoked/expired. Same as above; a newly configured token recovers without restart. |
| `connection_suspended` | 409 | Connection paused or suspended. Keep sending hello (it still authenticates); push nothing until `paused` clears. |
| `sync_in_progress` | 409 | Another request for this connection is in flight (stuck slot or duplicate agent). Short pause, bounded retries. |
| `unknown_repo` | 404 | Repo not active on this connection (deactivated mid-flight). Skip it; the next hello won't list it. |
| `invalid_schema` | 400 | The request body is malformed. Deterministic — do not retry unchanged. |
| `payload_too_large` | 413 | Body over cap. Halve the batch (floor 1) and resend. |
| `rate_limited` | 429 | Honor the `Retry-After` header **exactly**. |
| `protocol_unsupported` | 426 | This protocol major was retired. Log loudly, keep polling hello slowly, tell the operator to upgrade. |

## Rate limits

Per connection, sized to plan cadence (hourly windows on paid/trial plans, daily
windows on Free, elevated while a repo is still backfilling). Every `429` carries
`Retry-After` in seconds. The reference agent honors short windows inline
(sleep + retry) and abandons the repo for the cycle on long windows — the repo
stays due server-side and resumes automatically, while the hello heartbeat keeps
the connection visibly alive.

## `POST /ingest/v1/hello/` — heartbeat + the entire control plane

Send it every `poll_interval_seconds` (server-driven; default 60). Hello doubles as
liveness — the dashboard's online/offline state derives from it. A paused
connection still authenticates hello; only the data endpoints refuse it.

Request:

```json
{
  "protocol": 1,
  "instance_id": "hostname-3f2a9c1b",
  "agent": {"version": "1.0.0", "os": "linux", "arch": "amd64"},
  "collector": {
    "kind": "gitlab_ci",
    "base_url": "https://gitlab.internal.example.com",
    "version": "17.8.1",
    "reachable": true,
    "error": ""
  },
  "repos": [
    {"external_id": "42", "status": "ok", "code": "", "detail": "18 runs pushed"}
  ]
}
```

- `instance_id`: stable per process, ≤ 64 chars. Two agents alternating on one token
  are detected through it and surfaced to the user.
- `collector`: report reality — `reachable: false` with an error string lets the
  dashboard distinguish "agent up, GitLab down" from "agent down".
- `repos`: outcomes of the PREVIOUS cycle, at most 50. `status` is `ok`, `error` or
  `skipped`. `code`/`detail` (≤ 200 chars) are advisory; the reference agent uses
  codes like `partial`, `rate_limited`, `unknown_repo`, `push_failed`,
  `collector_error`, `bad_cursor`, `aborted`.

Response:

```json
{
  "protocol": 1,
  "min_agent_version": "1.0.0",
  "connection": {"name": "HQ GitLab", "paused": false},
  "poll_interval_seconds": 60,
  "sync_now": false,
  "catalog_requested": false,
  "backfill_not_before": "2026-06-27T00:00:00+00:00",
  "limits": {
    "max_runs_per_batch": 100,
    "max_body_bytes": 1048576,
    "log_tail_chars": 8000,
    "max_logs_per_request": 20
  },
  "repos": [
    {"external_id": "42", "name": "acme/api", "sync_due": true,
     "cursor": "2026-08-25T21:14:09+00:00", "initial_sync_completed": true}
  ],
  "want_logs": [
    {"repo_external_id": "42", "run_external_id": "9912", "job_external_id": "40071"}
  ]
}
```

- `catalog_requested`: also `true` automatically for a connection that has never
  completed a catalog reconcile — this is what populates the project picker during
  onboarding with no dashboard action.
- `repos` lists only repositories activated in the dashboard. `sync_due` is computed
  per repo from plan cadence; a newly activated repo (null cursor) is always due.
- `sync_now: true` (the dashboard button) makes every listed repo due once; the flag
  is consumed by this hello.
- `min_agent_version` is a soft floor: warn the operator, never exit.
- `want_logs` is the server's *current* outstanding list, recomputed from its own
  state (see logs below).

## `POST /ingest/v1/catalog/`

Send when `catalog_requested` is true: the full list of projects the credential can
see, capped at 2000 entries — set `"truncated": true` if the instance has more (the
server then skips orphan-flipping for safety).

```json
{"protocol": 1, "collector": {…}, "truncated": false,
 "projects": [{"id": 42, "path_with_namespace": "acme/api"}]}
```

Response: `{"discovered": n, "renamed": n, "orphaned": n, "name_conflicts": n}`.
Projects are reconciled by `(connection, id)` — never by name — and appear in the
dashboard picker deactivated until a user activates them.

## `POST /ingest/v1/runs/` — the workhorse

One repository per request, at most `max_runs_per_batch` runs, raw provider
vocabulary (GitLab status strings go over the wire untranslated — the server owns
normalization, so a mapping fix is a server deploy, not an agent release).

```json
{
  "protocol": 1,
  "repo_external_id": "42",
  "final": false,
  "runs": [
    {"id": 9912, "iid": 812, "name": "Nightly build", "status": "failed",
     "source": "push", "ref": "main",
     "web_url": "https://gitlab.internal.example.com/acme/api/-/pipelines/9912",
     "created_at": "2026-08-25T11:02:33.000Z", "updated_at": "2026-08-25T11:09:41.000Z",
     "started_at": "2026-08-25T11:02:40.000Z", "duration": 421,
     "user": {"id": 7, "username": "mkowalski"},
     "jobs": [
       {"id": 40071, "name": "pytest", "status": "failed",
        "started_at": "2026-08-25T11:02:41.000Z",
        "finished_at": "2026-08-25T11:07:41.000Z", "duration": 300}
     ]}
  ]
}
```

Field rules (server-validated):

- Required per run: `id` (decimal string/int, ≤ 20 digits), `status` (non-empty
  string), `ref` (key must be present), `created_at`, `updated_at` (ISO-8601 with
  timezone). Timestamps more than 60 min in the future reject the row.
- Optional: `iid`, `name`, `source`, `web_url`, `started_at`, `duration`,
  `user{id, username}`, `jobs[]`.
- **Unknown fields are dropped and counted** server-side. There are no fields for
  file contents, diffs, or CI YAML — the schema half of zero-code-access.
- `web_url` must be on the connection's configured host; foreign hosts are stripped
  (never row-fatal).
- Jobs are stored only for failed completed runs (in GitLab vocabulary: statuses
  `failed` / `canceled`); sending jobs for other runs wastes bandwidth. At most 200
  jobs per run are read.
- Unknown status strings degrade gracefully (stored unclassified, counted) — a new
  provider status never wedges ingest.

Response:

```json
{"accepted": 24,
 "rejected": [{"id": 9899, "code": "invalid_timestamp", "field": "updated_at"}],
 "cursor": "2026-08-25T11:09:41+00:00",
 "want_logs": [{"repo_external_id": "42", "run_external_id": "9912",
                "job_external_id": "40071"}]}
```

Rejection codes: `invalid_schema`, `invalid_id`, `invalid_timestamp`,
`future_timestamp`, `missing_field`.

**Cursor semantics** (server-side, but load-bearing for agents):

- The cursor advances to `max(updated_at)` over accepted rows AND rejected rows
  whose `updated_at` still parses — so a malformed row is genuinely skipped rather
  than re-pulled forever (the poison-pill rule).
- The advance is clamped to server-now + 5 min and is monotonic. The payload cannot
  dictate the cursor.
- Upserts are idempotent on `(repository, run id)` / `(run, job id)`: any batch may
  be replayed any number of times. At-least-once delivery is the design.
- `"final": true` marks the agent's LAST batch for this repo this cycle and stamps
  the repo's cadence timestamp. A repo that never receives its final flag stays
  `sync_due` — this is what makes crashes safe.
- **The empty final batch**: a due repo with zero new runs must still receive one
  `{"final": true, "runs": []}` push, or it would stay due (and be re-walked) every
  cycle forever.

## `POST /ingest/v1/logs/`

Drain the `want_logs` list (from hello and runs responses): fetch each job's log
tail locally and push the last `log_tail_chars` characters, at most
`max_logs_per_request` entries per request, one repository per request.

```json
{"protocol": 1, "repo_external_id": "42", "logs": [
  {"run_external_id": "9912", "job_external_id": "40071", "tail": "…last 8000 chars…"},
  {"run_external_id": "9913", "job_external_id": "40099", "unavailable": true}
]}
```

- `unavailable: true` reports a trace the provider has purged (GitLab 404). **It
  still requires both `run_external_id` and `job_external_id`** — the server writes
  an empty log row so the job permanently drops out of `want_logs`.
- Response: `{"stored": n, "skipped": n, "want_logs": […]}` — `want_logs` is the
  remaining outstanding list.
- `want_logs` is **database state, not batch state**: the server recomputes it
  (failed jobs of active repos within the log retention window with no stored log)
  on every hello and runs response. Agents keep no bookkeeping; a crash, a rate
  limit, or a > cap backlog simply drains over subsequent cycles. Stop a drain that
  makes no progress (`stored: 0` and an unchanged list) and let the next cycle retry.

## Pulling from GitLab: filter-keyset pagination

How the reference agent walks `GET /api/v4/projects/:id/pipelines` — normative for
gap-free syncing:

```
GET …/pipelines?updated_after=<X>&order_by=updated_at&sort=asc&per_page=100
```

1. Start at `X = cursor` (from hello), or `X = backfill_not_before` when the cursor
   is null.
2. Push each page as one batch (splitting to honor `max_runs_per_batch` /
   `max_body_bytes`).
3. After a **full** page, re-issue the query with
   `X = max(updated_at of the page) − 1 s`. The 1-second overlap re-delivers
   boundary rows; idempotent upserts absorb them.
4. **Same-second fallback**: if a full page cannot advance the filter (all rows share
   one `updated_at`, or the new floor would not move), fall back to offset
   pagination (`page=2, 3, …`) within the current filter, and resume keyset as soon
   as the floor can advance again.
5. A non-full (or empty) page ends the walk — that batch (or an empty batch) carries
   `"final": true`.

Never use plain offset pagination across the whole window: a row whose `updated_at`
changes mid-scan shifts positions, and with ascending order a skipped row's
unchanged timestamp lands *behind* the cursor — a permanent, silent gap.
Filter-keyset is immune. Ascending order makes crash recovery contiguous: the
server-side cursor only ever covers a prefix it actually accepted.

Rows whose `updated_at` is missing or unparseable are **dropped agent-side** (and
reported in the next hello's outcomes): the server would reject them anyway, and
they must never be resent expecting cursor progress.

**The cycle rule**: `sync_due` gates *starting* a repo. Once started, drive the repo
to exhaustion — honoring 429s — regardless of due flags in any interleaved hello.

## Backoff and recovery rules

| Situation | Behavior |
|---|---|
| Connection error / 5xx | Exponential backoff, base 5 s, cap 15 min, jittered. |
| `429` | Honor `Retry-After` exactly (inline for short windows; abandon the repo for the cycle on long windows — it stays due). |
| `413` | Halve the batch, floor 1. |
| Two consecutive 5xx/timeouts for the *same* batch | Halve it too (a batch that consistently exceeds the server's processing window would otherwise livelock at full size). |
| A single-run batch that still fails | Skip it, report it in the next hello (poison-pill rule, agent half). It is retried naturally next cycle. |
| `401` | Log actionably (with the dashboard URL), retry slowly, never exit; recover without restart once a valid token is configured. |
| `409 connection_suspended` | Stop pushing; keep hello polling. |
| `409 sync_in_progress` | Short pause (~30 s), a few retries, then leave the repo for the next cycle. |
| `426` | Log loudly, poll hello slowly, wait for an operator upgrade. |

## Versioning & compatibility

- The major version lives in the URL (`/ingest/v1/`) and as `"protocol": 1` in every
  request body.
- Within a major, evolution is **additive only**. The server drops-and-counts
  unknown request fields; agents MUST ignore unknown response fields. Old agents
  keep working for the lifetime of the major.
- A breaking change ships as `/ingest/v2/` alongside v1 through a published sunset
  window; a retired version answers `426 protocol_unsupported`.
- `min_agent_version` in hello is a soft floor — a nudge, never a brick.
