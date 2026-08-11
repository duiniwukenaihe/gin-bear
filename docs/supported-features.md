# Supported Features

Supported: HTTP lifecycle, config, IoC, routing/binding/errors, JWT hooks,
GORM, Redis, migrations, cron, WebSocket, health, metrics, tracing, OpenAPI.
Experimental: dynamic Go plugins.
Compatibility-only: gRPC, MQ providers, WAF, GeoIP, BigQuery, schema,
config center, circuit breaker, and ID generator.

Supported features are maintained production capabilities. Experimental features
may change as their operational model matures. Compatibility-only features keep
their existing public APIs and accepted configuration keys for v0 consumers, but
new applications should not adopt them.
