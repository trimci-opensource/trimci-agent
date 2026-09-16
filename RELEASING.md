# Releasing

For maintainers. A release is a pushed `vX.Y.Z` tag; everything else is automated by
[`.github/workflows/release.yml`](.github/workflows/release.yml) (GoReleaser: binaries
for linux/darwin/windows, `checksums.txt`, and the multi-arch image
`ghcr.io/trimci-opensource/trimci-agent` with the tag ladder `X.Y.Z`, `X.Y`, `X`, `latest`).

## Versioning

- SemVer. The **major** tracks the ingest protocol major: the TrimCI dashboard pins
  `ghcr.io/trimci-opensource/trimci-agent:1`, so protocol v2 ships as agent 2.0.0.
- Patch = bug fixes only; minor = new behavior that stays compatible with protocol v1.
- Tags are immutable. A bad release is fixed forward by the next patch version —
  never re-tag or force-push a tag that a workflow has already published.

## Checklist

1. `main` is green (CI: gofmt, vet, test, build matrix, govulncheck).
2. `CHANGELOG.md`: move the **Unreleased** entries under a new `## [X.Y.Z] - YYYY-MM-DD`
   heading, leave an empty **Unreleased** section, update the compare links.
3. Commit (`docs: changelog for X.Y.Z`) and merge to `main`.
4. Tag and push:

   ```bash
   git tag -a vX.Y.Z -m "TrimCI Agent X.Y.Z"
   git push origin vX.Y.Z
   ```

5. Watch the *Release* workflow, then verify:
   - the GitHub release lists five archives + `checksums.txt`;
   - `docker run --rm ghcr.io/trimci-opensource/trimci-agent:X.Y.Z -version` prints `trimci-agent/X.Y.Z`;
   - `docker buildx imagetools inspect ghcr.io/trimci-opensource/trimci-agent:X` shows `linux/amd64`
     and `linux/arm64`;
   - `sha256sum -c checksums.txt --ignore-missing` passes for a downloaded archive.
6. If the server should stop accepting older agents, raise
   `AGENT_MIN_SUPPORTED_VERSION` on trimci.com (a soft floor — it only warns).

## Toolchain

- Workflows build with an explicit `go-version` (`1.27.x` today) rather than the
  `go.mod` minimum: the minimum is a language-compatibility promise for contributors,
  while a release must be compiled with a Go line that still receives security fixes.
  When a new Go major ships, bump `go-version` in both workflows, `go-version-input`
  in the govulncheck job, `FROM golang:` in the Dockerfile and the example in
  CONTRIBUTING.md — then run the dry run below.
- GoReleaser is pinned to an exact version in `release.yml`. The `dockers` /
  `docker_manifests` sections are deprecated upstream in favor of `dockers_v2`; migrate
  before bumping the pin past a version that removes them, and dry-run the docker
  stage on a host with a Docker socket.

## Dry run

Without a Go toolchain, validate the release configuration and cross-compile
everything locally (no images, nothing published):

```bash
docker run --rm --entrypoint sh -v "$PWD:/src" -w /src goreleaser/goreleaser:latest -c \
  'git config --global --add safe.directory /src && goreleaser check && goreleaser release --snapshot --clean --skip=docker,publish'
```
