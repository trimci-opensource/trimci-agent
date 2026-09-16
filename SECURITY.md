# Security policy

## Reporting a vulnerability

Email **office@trimci.com** (see also
[trimci.com/.well-known/security.txt](https://trimci.com/.well-known/security.txt)).

## Security model (short version)

- **Outbound-only**: the agent opens no listening ports (the optional `/healthz`
  endpoint binds only where you explicitly configure it).
- **Zero code access, enforced in the binary**: every GitLab request is checked
  against the allowlist in
  [`internal/collector/gitlabci/scope.go`](internal/collector/gitlabci/scope.go)
  before it leaves the process. There is no code path that reads repository
  contents, merge requests, commits, or CI YAML. The TrimCI ingest API enforces the
  same boundary independently with a schema that has no fields for such data.
- **Credential handling**: your GitLab token is only ever sent to your configured
  `GITLAB_URL`. The TrimCI agent token is sent only to `TRIMCI_URL`, over TLS;
  TrimCI stores only its SHA-256 hash. The agent's log stream is scrubbed of
  configured secrets and credential-shaped strings.
- **Stateless**: nothing is written to disk — no spool files, no cursor files, no
  copies of your data at rest inside your network.
- **Zero third-party dependencies**: the module has no external requirements
  (`go.mod` lists none), so there is no supply chain beyond the Go toolchain and
  this repository's ~2,000 auditable lines.
- **Verifiable releases**: binaries ship with SHA-256 checksums; images are
  published to `ghcr.io/trimci-opensource/trimci-agent` from the tagged source via GitHub Actions.
