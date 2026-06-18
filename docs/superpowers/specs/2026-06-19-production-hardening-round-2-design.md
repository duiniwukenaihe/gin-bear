# Production Hardening Round 2 Design

**Goal:** Improve the scaffold's production safety around request binding, direct route registration, rate limiter degradation, WebSocket origin checks, repository updates, and dynamic plugin loading.

**Scope:** This round focuses on runtime behavior that can cause incorrect requests, unsafe defaults, or surprising production exposure. It intentionally avoids broad observability, migration tooling, and OpenAPI expansion, which remain separate follow-up work.

## Approach

The framework should preserve existing lightweight ergonomics while making unsafe production paths explicit.

- Request binding should bind URI path parameters first, then query/form fields, then JSON or form body according to request method and content type. A request struct may combine `uri`, `form`, `query`, `json`, and `binding` tags without forcing the entire struct into one source.
- `Bear.Handle`, `HandleWithFairing`, and `HandleWS` should work when called directly after `Ignite`, without requiring `Mount` or `ApplyAll` first.
- Redis rate limiting should keep the old fail-open default for compatibility, but expose a `FailClosed` option for sensitive deployments.
- WebSocket origin checks should support an explicit allowlist. Disabling origin checks in production should require deliberate configuration and should be rejectable by production validation unless an allowlist exists.
- Repository updates should avoid non-versioned full-row `Save` by using GORM `Updates` on the model. Existing `UpdateByID` remains the safer patch API.
- Dynamic plugin loading should be disabled by default and restricted to configured directories when enabled.

## Testing

Tests should be added before code changes:

- Binding mixed URI/query/JSON sources.
- Direct `Handle` route registration without `ApplyAll`.
- Redis limiter fail-open and fail-closed behavior when Redis is unavailable.
- WebSocket origin allowlist helper behavior and production validation.
- Repository non-versioned update not zeroing omitted fields.
- Plugin loading disabled by default and path allowlist enforcement.

## Compatibility

Existing projects keep working unless they run production mode with explicitly unsafe WebSocket origin settings or dynamic plugins without enabling the new plugin config. Those cases should fail early with clear errors.
