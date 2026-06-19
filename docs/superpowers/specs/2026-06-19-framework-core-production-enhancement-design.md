# Framework Core Production Enhancement Design

## Goal

Improve gin-bear as a framework scaffold, not as a deployment bundle. This batch focuses on generated application correctness, configurable production runtime behavior, and framework-level verification.

## Scope

In scope:
- `bear gen api` output correctness and safety.
- Runtime configuration for shutdown, readiness, logging, Redis dependency behavior, and common environment overrides.
- Documentation and examples for framework-level production usage.
- Tests that prove generated code and runtime configuration behave correctly.

Out of scope:
- Docker, Compose, Kubernetes, container images, SBOM generation, and platform deployment manifests.
- Broad rewrites of IoC, routing, persistence, tracing, or plugin architecture.

## Design

Generated CRUD code should be immediately useful after scaffolding. Create handlers must copy request DTO fields into models. Update handlers should avoid overwriting omitted fields with Go zero values by using pointer fields in update DTOs and building update maps only for provided values. Pagination should normalize page and page size with conservative defaults and maximums.

Runtime behavior should keep existing defaults while allowing production services to tune key values. `server.shutdown_timeout` controls graceful HTTP shutdown. `health.readiness_timeout` controls dependency readiness checks. `log.level` controls the default slog level. Redis can be marked required so strong dependencies fail fast at startup. Environment variables cover the common production knobs without forcing users to maintain multiple config files.

## Validation

Tests must cover:
- Field parsing for framework generator types such as `email`, `url`, `phone`, `text`, `decimal`, and `datetime`.
- Generated API packages compile and contain DTO-to-model mapping, pointer update DTOs, update map guards, and pagination normalization.
- Generated names are safe for dashed, underscored, spaced, punctuated, and digit-prefixed resource names.
- Runtime configuration validates new duration fields and applies them to server shutdown/readiness behavior.
- Logger level configuration changes emitted slog level.
- Redis required mode reports startup connection failures.
- Documentation and release checks remain focused on framework behavior.
