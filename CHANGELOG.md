# Changelog

All notable changes to the TrimCI Agent. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/).

## [Unreleased]

### Added

- Initial agent: hello control loop, GitLab CI collector with the
  zero-code-access scope allowlist, filter-keyset pipeline pagination with
  same-second fallback, push pipeline (catalog / runs with the final flag /
  want_logs drain), Retry-After honoring, batch halving, poison-pill skip,
  token redaction in logs, optional `/healthz`, `-once` cron mode.
- Protocol v1 documentation (`docs/PROTOCOL.md`), release engineering
  (goreleaser, multi-arch `ghcr.io/trimci/agent` image, checksums).
