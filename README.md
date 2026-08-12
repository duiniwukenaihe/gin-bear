# Gin-Bear

Gin-Bear is a Gin-based application scaffold and framework, evolved from the
controller, IoC, Fairing, and responder model of
[goft-gin](https://github.com/shenyisyn/goft-gin). It adds production-oriented
lifecycle, configuration, authentication, health, metrics, tracing, and
OpenAPI support.

## Add The Framework

Add the current release to a Go application:

```bash
go get github.com/duiniwukenaihe/gin-bear@v0.9.3
```

Application code imports the runtime from
`github.com/duiniwukenaihe/gin-bear/pkg/bear`. `v0.9.3` is the current release;
GitHub publishes the immutable source tag and the Go toolchain resolves it as a
module dependency.

## Generate A Project (Optional)

`cmd/bear` is an optional project generator, not the framework runtime. Use it
when starting a new service from the maintained application structure:

```bash
go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.3
bear new my-service
```

The generated project's `go.mod` pins the same `gin-bear` framework version.
Existing applications do not need to install the generator.

When running an unversioned development build of the CLI, select the framework
dependency explicitly so generated code cannot silently target another version:

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
- [v0.9.1 to v0.9.2 migration guide](docs/migration-v0.9.1-to-v0.9.2.md)
- [Compatibility contract](docs/compatibility.md)
- [Production runbook](docs/runbook.md)
- [Security policy](SECURITY.md)
- [Changelog](CHANGELOG.md)

Before a release, run the complete local gate with the project Go version:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify
```
