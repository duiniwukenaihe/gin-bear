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
`github.com/duiniwukenaihe/gin-bear/pkg/bear`. `v0.9.3` is the current release.
The production gRPC work described in this checkout remains under
[Unreleased](CHANGELOG.md#unreleased) until it is reviewed, verified, and
included in a future tag.

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

## Recommended Startup

The first-choice path for new applications is `IgniteE`, error-returning
registration, and `Serve`. The process owns signal handling and records the
single error returned from startup or serving:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

func run(ctx context.Context) error {
	config := bear.NewSysConfig()
	config.SetFrameworkStrict(true)
	application, err := bear.IgniteE(config)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	if err := application.HandleE("GET", "/hello", func() string { return "hello" }); err != nil {
		return fmt.Errorf("register hello route: %w", err)
	}
	if err := application.EnableHealthE(); err != nil {
		return fmt.Errorf("initialize health: %w", err)
	}
	if err := application.Serve(ctx); err != nil {
		return fmt.Errorf("serve application: %w", err)
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

`Ignite`, fluent registration, and `Launch` remain available for v0 source
compatibility, but they are not the recommended path for new services.

## Runnable Examples

The examples below are source files, not copied snippets. They are compiled by
`go test ./...`, so their APIs stay aligned with the framework.

- [Basic lifecycle and cancellable serving](examples/basic/main.go): uses
  `IgniteE`, `MountE`, `EnableHealthE`, and `Serve`.
- [Typed token-revocation error handling](examples/auth/main.go): detects
  `bear.ErrTokenRevocationUnavailable` with `errors.Is`.
- [Production configuration loading](examples/migration/main.go): handles the
  error-returning `bear.LoadConfig` API before application startup.

Run the basic service with `go run ./examples/basic` and open
`http://localhost:8080/api/hello`.

## Optional gRPC Runtime

The unreleased source tree defines `GRPCServiceRegistrar` for injectable
services and the error-returning registration methods `AddGRPCServiceE`,
`AddGRPCUnaryInterceptorE`, and `AddGRPCStreamInterceptorE`. Register at least
one business service before enabling gRPC. Authentication belongs in unary and
stream interceptors; HTTP Fairings do not run for gRPC calls.

gRPC is disabled by default. Its standard health service is enabled by default,
while reflection is disabled by default. Production deployments must choose one
explicit transport: process-owned TLS, process-owned mTLS, or plaintext bound to
loopback behind a same-host Nginx/Envoy TLS terminator. See the
[production guide](docs/production.md#grpc) for configuration and limits.

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
