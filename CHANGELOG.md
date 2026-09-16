# Changelog

All notable changes to the TrimCI Agent. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-09-16

### Added

- Initial agent: hello control loop, GitLab CI collector with the zero-code-access
  scope allowlist, filter-keyset pipeline pagination with same-second fallback, push
  pipeline (catalog / runs with the final flag / want_logs drain), Retry-After
  honoring, batch halving, poison-pill skip, token redaction in logs, optional
  `/healthz`, `-once` cron mode.
- Protocol v1 documentation (`docs/PROTOCOL.md`), release engineering (goreleaser,
  multi-arch `ghcr.io/trimci-opensource/trimci-agent` image, checksums), open-source
  hygiene (Dependabot, issue templates, CONTRIBUTING, RELEASING, govulncheck in CI).

[Unreleased]: https://github.com/trimci-opensource/trimci-agent/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/trimci-opensource/trimci-agent/releases/tag/v1.0.0
