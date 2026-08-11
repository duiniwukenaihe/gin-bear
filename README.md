# Gin-Bear

Gin-Bear is a Go web framework built on Gin with application lifecycle,
configuration, authentication, health, metrics, tracing, and OpenAPI support.

## Install The CLI

Install the currently published generator CLI from its canonical package path:

```bash
go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.1
bear new my-service
```

The next command is usable only after publication:

```bash
go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.3
```

The current development worktree is an unpublished `v0.9.3` candidate that
includes the `v0.9.2` runtime-hardening checkpoint. It has not been pushed,
tagged, or published; continue to use `v0.9.1` until a release is published.

When running a development build of the CLI, select the framework dependency
explicitly so generated code cannot silently target an older incompatible
version:

```bash
bear new my-service --framework-version v0.9.3
```

## Runnable Examples

The examples below are source files, not copied snippets. They are compiled by
`go test ./...`, so their APIs stay aligned with the framework.

- [Basic lifecycle and cancellable launch](examples/basic/main.go): handles
  `ApplyAll` and `Launch` errors and installs a cancellation-aware service.
- [Typed token-revocation error handling](examples/auth/main.go): detects
  `bear.ErrTokenRevocationUnavailable` with `errors.Is`.
- [Production configuration loading](examples/migration/main.go): handles the
  error-returning `bear.LoadConfig` API before application startup.

Run the basic service with `go run ./examples/basic` and open
`http://localhost:8080/api/hello`.

## Production And Upgrade Guidance

- [Production guide](docs/production.md)
- [v0.9.2 strict migration guide](docs/migration-v0.9-to-v0.10.md)
- [Compatibility contract](docs/compatibility.md)
- [Production runbook](docs/runbook.md)
- [Security policy](SECURITY.md)
- [Changelog](CHANGELOG.md)

Before a release, run the complete local gate with the project Go version:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify
```
