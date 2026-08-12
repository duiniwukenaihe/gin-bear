# Supported Features

Supported: HTTP lifecycle, config, IoC, routing/binding/errors, JWT hooks,
GORM, Redis, migrations, cron, WebSocket, health, metrics, tracing, OpenAPI,
and optional gRPC service registration/runtime.
Experimental: dynamic Go plugins.
Compatibility-only: MQ providers, WAF, GeoIP, BigQuery, schema, config center,
circuit breaker, ID generator, and the legacy `GRPCService` interface.

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
