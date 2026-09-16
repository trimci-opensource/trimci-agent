# Changelog

All notable changes to the TrimCI Agent. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/).

## [Unreleased]

### Changed

- Repository and image moved to `github.com/trimci-opensource/trimci-agent` /
  `ghcr.io/trimci-opensource/trimci-agent`; release engineering hardened: Go 1.27
  toolchain, GoReleaser pinned, distroless base pinned by digest, OCI labels,
  govulncheck in CI, Dependabot, issue templates, CONTRIBUTING and RELEASING guides.
- Docs and log wording: a revoked TrimCI token is never un-revoked server-side,
  so recovery is a restart with the newly minted token (the agent keeps retrying
  and never exits meanwhile); the 426 contract is the stable error envelope.

### Added

- Initial agent: hello control loop, GitLab CI collector with the
  zero-code-access scope allowlist, filter-keyset pipeline pagination with
  same-second fallback, push pipeline (catalog / runs with the final flag /
  want_logs drain), Retry-After honoring, batch halving, poison-pill skip,
  token redaction in logs, optional `/healthz`, `-once` cron mode.
- Protocol v1 documentation (`docs/PROTOCOL.md`), release engineering
  (goreleaser, multi-arch `ghcr.io/trimci-opensource/trimci-agent` image, checksums).
