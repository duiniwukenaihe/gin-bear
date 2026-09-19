# Supported Features

Supported: HTTP lifecycle, config, IoC, routing/binding/errors, JWT hooks,
GORM, Redis, migrations, cron, WebSocket, health, metrics, tracing, OpenAPI,
and optional gRPC service registration/runtime.
Experimental: dynamic Go plugins.
Compatibility-only: MQ providers, WAF, GeoIP, BigQuery, schema, config center,
circuit breaker, ID generator, the legacy `GRPCService` interface, and the
`pkg/bear/gen` code generator.

`pkg/bear/gen` is retained because v0.9.1 shipped it and the v0.9.1 API baseline
pins it. New code generation belongs to the `bear gen` CLI command, which owns
the supported generators.

Its scanner renders struct and field names without a package qualifier, because
`NewGenerator` is given only a package name and cannot build an import path, so
the file it renders has to be written into the package it was scanned from.
Generating into a different package does not compile.

Supported features are maintained production capabilities. Experimental features
may change as their operational model matures. Compatibility-only features keep
their existing public APIs and accepted configuration keys for v0 consumers, but
new applications should not adopt them.

The optional gRPC runtime uses `GRPCServiceRegistrar`, error-returning service
and interceptor registration, TLS/mTLS or explicitly loopback-only plaintext,
bounded messages/connections, recovery and access logging. Standard health is
enabled by default and reflection is disabled by default. HTTP Fairings do not
apply to gRPC. gRPC-Gateway, service discovery, load balancing, protobuf
generation, and client SDK generation are outside the supported boundary.

This classification describes the unreleased source tree. It is not a claim
that the gRPC production runtime has shipped in an existing tag.
