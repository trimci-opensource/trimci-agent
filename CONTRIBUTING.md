# Contributing

Thanks for looking under the hood. This agent is deliberately small so that anyone
can audit it; keeping it that way is the first rule of contributing.

## Ground rules (non-negotiable)

1. **Zero third-party dependencies.** `go.mod` lists none and must stay that way —
   pull requests that add a `require` are declined, however good the library. The
   standard library is enough for an HTTP client, JSON, TLS and logging.
2. **The scope allowlist is the product.** Every GitLab URL the agent can request is
   listed in [`internal/collector/gitlabci/scope.go`](internal/collector/gitlabci/scope.go)
   and mirrored in the README table. Adding an endpoint is a policy change, not a
   code change: it needs the allowlist + its test, the README table, the matching
   server-side allowlist on trimci.com, and a privacy-policy update. Open an issue
   first so the change can be discussed before any code is written.
3. **The protocol evolves additively.** Within protocol v1 the agent must keep working
   against older and newer servers: ignore unknown response fields, never require a
   new one. The wire contract is [`docs/PROTOCOL.md`](docs/PROTOCOL.md); the server's
   contract tests live in the TrimCI application repository.
4. **Nothing is written to disk.** The agent is stateless by design — no spool files,
   no cursor files, no caches.

## Developing

You need either Go 1.24+ or Docker.

```bash
go build ./cmd/trimci-agent && go test ./...
# or, without a Go toolchain:
docker run --rm -v "$PWD:/src" -w /src golang:1.27 sh -c 'gofmt -l . && go vet ./... && go test ./...'
```

`gofmt` and `go vet` must be clean (CI enforces both); `govulncheck` runs in CI as well.
To try a change end to end, point the agent at a TrimCI connection with
`TRIMCI_URL` (see the configuration table in the README).

## Pull requests

- One change per PR, with a test when behavior changes.
- Conventional commit prefixes (`feat:`, `fix:`, `docs:`, `chore:`, `test:`) and a
  short body explaining *why*.
- Update `CHANGELOG.md` under **Unreleased** for anything user-visible.
- By contributing you agree that your contribution is licensed under the
  repository's [MIT license](LICENSE). No CLA.

## Security

Please report vulnerabilities privately — see [SECURITY.md](SECURITY.md).
