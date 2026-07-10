# Compatibility Contract

## v0 Public Surface

The v0 public API remains available, including `Ignite`, `GetInjector`,
`GetByType`, `Handle`, `Mount`, `ApplyAll`, `Launch`, the existing config
structs, and existing adapter constructors. Existing YAML keys remain accepted,
including compatibility-only keys, so an existing application can continue to
compile and load its configuration while it plans a migration.

## Feature States

See [supported features](supported-features.md) for the supported,
experimental, and compatibility-only categories. Compatibility-only APIs are
annotated with Go `Deprecated:` comments. They are retained for source and
configuration compatibility, not for new feature work.

## Compatibility-only Configuration

`Ignite` logs one structured warning for every enabled compatibility-only
configuration that is currently a no-op. The warning keys are `waf`, `geoip`,
`bigquery`, `mq`, `kafka`, `rocketmq`, `pulsar`, `schema`,
`circuit_breaker`, and `config_center`. Warnings are deduplicated within each
startup.

The ID generator is retained as a deprecated method without an enabled config
flag. gRPC remains a compatibility-only API; its current launch path is not a
no-op, so enabling it does not produce a no-op warning.
