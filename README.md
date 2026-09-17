# TrimCI Agent

[![CI](https://github.com/trimci-opensource/trimci-agent/actions/workflows/ci.yml/badge.svg)](https://github.com/trimci-opensource/trimci-agent/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/trimci-opensource/trimci-agent)](https://github.com/trimci-opensource/trimci-agent/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A single-binary, stateless, open-source agent that brings [TrimCI](https://trimci.com)'s
CI/CD failure analysis to GitLab instances behind VPNs and private networks — without
opening a single inbound port.

The agent runs **inside your network**, pulls pipeline observability data (runs, jobs,
timing, failed-job log tails) from your local GitLab API, and pushes it **outbound-only
over HTTPS** to TrimCI's ingest API.

- ✓ Runs in your network; only outbound HTTPS to trimci.com
- ✓ No inbound ports, no firewall changes
- ✗ Your GitLab token never leaves your network — TrimCI never sees or stores it

The agent is deliberately small (~2,000 lines of Go, **zero third-party dependencies**)
so you can audit every line before you run it. The URL allowlist in
[`internal/collector/gitlabci/scope.go`](internal/collector/gitlabci/scope.go) is,
verifiably, every URL this agent can request.

## 60-second quickstart

1. In the TrimCI dashboard, add a connector: **GitLab → Behind a VPN / private
   network**. You get an agent token (shown once) and a ready-to-run `docker run`
   command with the token pre-filled.
2. Create a GitLab access token (personal, group, or project) with the `read_api`
   scope — *Edit profile → Access tokens*, or *Settings → Access tokens* on a group or
   project. It stays on your machine: the agent only ever sends it to your GitLab.
3. Run the agent on any machine inside your network that can reach GitLab and has
   outbound HTTPS to trimci.com — a small VM, the runner host, or a laptop for a trial.
   Docker Desktop on Windows and macOS works the same as Docker on Linux (the image
   ships for `linux/amd64` and `linux/arm64`, so Apple Silicon runs it natively).

**Linux / macOS** (bash or zsh):

```bash
docker run -d --restart unless-stopped --name trimci-agent \
  -e TRIMCI_TOKEN='trimci_agent_…' \
  -e GITLAB_URL='https://gitlab.your-company.internal' \
  -e GITLAB_TOKEN='YOUR-READ-API-PAT' \
  ghcr.io/trimci-opensource/trimci-agent:1
```

**Windows** (PowerShell — the backtick is PowerShell's line continuation; one long line
without backticks works too):

```powershell
docker run -d --restart unless-stopped --name trimci-agent `
  -e TRIMCI_TOKEN='trimci_agent_…' `
  -e GITLAB_URL='https://gitlab.your-company.internal' `
  -e GITLAB_TOKEN='YOUR-READ-API-PAT' `
  ghcr.io/trimci-opensource/trimci-agent:1
```

Keep the single quotes in both shells: they stop `$` and other special characters inside
the tokens from being interpreted. From `cmd.exe`, put everything on one line and use double
quotes instead.

4. Check the first log lines — the same command on every platform:

```bash
docker logs -f trimci-agent
```

You should see `agent starting …` followed by `catalog pushed …`. Within a minute the
dashboard page flips to **Connected**; pick the projects to track — done.

### docker compose

```yaml
services:
  trimci-agent:
    image: ghcr.io/trimci-opensource/trimci-agent:1
    restart: unless-stopped
    environment:
      TRIMCI_TOKEN: 'trimci_agent_…'
      GITLAB_URL: 'https://gitlab.your-company.internal'
      GITLAB_TOKEN: 'YOUR-READ-API-PAT'
```

### Static binary + systemd

Download the binary for your platform from the
[releases page](https://github.com/trimci-opensource/trimci-agent/releases) and verify it:

```bash
sha256sum -c checksums.txt --ignore-missing
sudo install -m 0755 trimci-agent /usr/local/bin/trimci-agent
```

`/etc/trimci-agent.env` (mode `0600`, owned by root):

```
TRIMCI_TOKEN=trimci_agent_…
GITLAB_URL=https://gitlab.your-company.internal
GITLAB_TOKEN=YOUR-READ-API-PAT
```

`/etc/systemd/system/trimci-agent.service`:

```ini
[Unit]
Description=TrimCI Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/trimci-agent
EnvironmentFile=/etc/trimci-agent.env
Restart=always
RestartSec=5
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now trimci-agent
```

Prefer cron? `trimci-agent -once` runs a single sync cycle and exits.

### Windows without Docker

Download `trimci-agent_<version>_windows_amd64.zip` from the releases page, check its hash
against `checksums.txt` (`Get-FileHash .\trimci-agent_<version>_windows_amd64.zip -Algorithm SHA256`),
unzip it, and run it from PowerShell:

```powershell
$env:TRIMCI_TOKEN = 'trimci_agent_…'
$env:GITLAB_URL   = 'https://gitlab.your-company.internal'
$env:GITLAB_TOKEN = 'YOUR-READ-API-PAT'
.\trimci-agent.exe
```

To keep it running unattended, start it from a small wrapper script (which sets the three
variables) registered in *Task Scheduler → Create Task → Triggers: At startup*, or wrap it as
a Windows service with a tool such as NSSM. `-once` works the same way for a scheduled task
that runs every few minutes.


## Configuration

Environment variables are the only configuration surface — everything behavioral
(poll interval, batch sizes, which projects to sync, from which cursors) is driven by
the server, so there is nothing else to configure or persist.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `TRIMCI_TOKEN` | yes¹ | — | Agent token from the TrimCI dashboard (shown once). |
| `TRIMCI_TOKEN_FILE` | — | — | Read the token from a file instead (secret managers). |
| `GITLAB_URL` | yes | — | Base URL of your GitLab instance. |
| `GITLAB_TOKEN` | yes¹ | — | GitLab access token, scope `read_api`. **Never sent to TrimCI.** |
| `GITLAB_TOKEN_FILE` | — | — | Read the GitLab token from a file instead. |
| `TRIMCI_URL` | — | `https://trimci.com` | Ingest API origin (override for sandbox testing). |
| `GITLAB_CA_BUNDLE` | — | — | PEM file with extra CA certificates for the GitLab side (internal CAs). Appended to the system pool. |
| `LOG_LEVEL` | — | `info` | `debug` / `info` / `warn` / `error`. |
| `LOG_FORMAT` | — | `text` | `text` / `json`. |
| `HEALTHZ_ADDR` | — | *(off)* | Serve `GET /healthz` on this address (e.g. `127.0.0.1:8080`). Off = the agent opens **no** listening ports. |
| `TRIMCI_ALLOW_HTTP` | — | `false` | Permit an `http://` `TRIMCI_URL`. Development only. |

¹ either the variable or its `_FILE` twin.

`HTTPS_PROXY` / `NO_PROXY` are honored for the TrimCI leg (standard Go behavior).

## What the agent sends — and what it never reads

The agent pushes **pipeline observability data only**:

- the project catalog (id + path) so you can pick which projects to track,
- pipeline runs (status, ref, source, timing, triggering username) and their jobs
  for failed runs,
- the last 8,000 characters of failed jobs' logs (for failure analysis).

It cannot read your source code — not by policy, but by construction. Every outbound
GitLab request is checked against this allowlist in
[`scope.go`](internal/collector/gitlabci/scope.go) before it leaves the process:

| Allowed URL | Why |
|---|---|
| `/api/v4/version` | reachability probe + version shown in the dashboard |
| `/api/v4/projects` (`membership=true&simple=true`) | project catalog: id + path |
| `/api/v4/projects/:id` | project metadata |
| `/api/v4/projects/:id/pipelines` | pipeline runs |
| `/api/v4/projects/:id/pipelines/:pid` | run detail (duration, user, timing) |
| `/api/v4/projects/:id/pipelines/:pid/jobs` | jobs of one pipeline |
| `/api/v4/projects/:id/jobs/:jid/trace` | failed-job log text |

Everything else — `/repository/*`, `/merge_requests`, `/commits`, `/snippets`,
`/groups`, raw files — is refused **inside the agent**, synchronously, before any
HTTP request is made. The server enforces the same boundary independently: TrimCI's
ingest API is schema-bounded and simply has no fields for file contents, diffs, or
CI YAML.

The same scope guard exists in TrimCI's server-side GitLab client; the agent's list
is a deliberate **subset** (it drops `/api/v4/user`, `/api/v4/personal_access_tokens/self`
and the bare single-job endpoint, which the agent does not need).

## Security properties

- **Outbound-only.** The agent listens on nothing (unless you opt into `HEALTHZ_ADDR`,
  which binds where you tell it, typically loopback).
- **Stateless.** No files, no database, no local queue. Sync cursors live server-side,
  so crash/restart/reinstall recovery is automatic, and a duplicated agent degrades to
  wasted work, not corruption.
- **Your GitLab credential stays home.** `GITLAB_TOKEN` is only ever sent to
  `GITLAB_URL`. TrimCI stores only a SHA-256 hash of the *agent* token.
- **Log hygiene.** The log stream is scrubbed of the configured secrets and anything
  shaped like a credential before it is written.
- **TLS required** for the TrimCI leg; internal CAs supported on the GitLab leg via
  `GITLAB_CA_BUNDLE`.

Found a vulnerability? See [SECURITY.md](SECURITY.md).

## Operations

- **Logs** go to stderr (12-factor); `LOG_FORMAT=json` for log collectors.
- **Health**: set `HEALTHZ_ADDR=127.0.0.1:8080` and probe `GET /healthz` (200 while
  hellos succeed, 503 otherwise). In a container, `trimci-agent -healthcheck` probes
  it for you (for `HEALTHCHECK` / compose `healthcheck` blocks).
- **Shutdown**: SIGTERM/SIGINT stop the agent gracefully. A batch killed mid-request
  is safe — the server's writes are idempotent.
- **Token rotation**: rotate in the dashboard (the old token keeps working for
  24 hours), update the env, restart the agent. A revoked token does **not** crash
  the agent — it logs an actionable error and keeps retrying slowly; restart it with
  the new token to resume (a revoked token is never un-revoked server-side).
- **Upgrades** are manual on purpose — you control what runs in your network. The
  dashboard shows a chip when a newer agent version is available.
- **One agent per GitLab instance.** Don't run two replicas on one token: the second
  one is rejected per-request (`409 sync_in_progress`) and the dashboard warns about
  duplicate agents.

## Building from source

Reproducing the released binary needs only Go 1.24+ (or Docker):

```bash
go build ./cmd/trimci-agent
# or, without a Go toolchain:
docker build -t trimci-agent .
```

Run the tests the same way:

```bash
go test ./...
# or:
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test ./...
```

## Protocol

The wire protocol (endpoints, schemas, pull rules, backoff rules) is documented in
[`docs/PROTOCOL.md`](docs/PROTOCOL.md) — complete enough to implement a compatible
agent from scratch. Within protocol v1 changes are additive-only: old agents keep
working.

## License

[MIT](LICENSE).
