# ADR-001: runtime agent model integration

Date: 2026-09-20. Status: accepted for M3 experimental use only.

## Decision

Drive the single read-only agent loop on eino core contracts
(`model.BaseChatModel` for Generate/Stream, `schema.Message`/`ToolCall` for
wire shape, `model.WithTools` for per-request tool binding), pinned to
`github.com/cloudwego/eino v0.9.19` (Apache-2.0, requires Go >= 1.18,
verified against Go 1.25.14).

The ReAct loop itself is ours, not eino's `flow/agent/react`: per-call
re-authorization, token/call/concurrency budgets, and fail-closed semantics
must sit inside the loop, where a borrowed loop would bypass them.

## Vendor strategy

- `FakeChatModel`: deterministic scripts for unit tests, evals, offline
  acceptance. No network, no key, ever.
- `OpenAIChatModel`: stdlib HTTPS against any OpenAI-compatible
  `/chat/completions` endpoint, adapted into eino schema messages (usage
  included). Constructed only when explicitly enabled with base URL, model,
  and key; disabled means no client and no dial.
- No other vendor SDK is vendored. A live-vendor call needs operator
  credentials and stays NOT_RUN until then; that evidence is "supplier
  reachable", never "production ready".

## Non-goals (explicit)

No vector store, no Redis queue, no Python service, no chat frontend, no
generic HTTP/shell/SQL/filesystem tools. Tenant isolation is a database
condition plus explicit authorization, not model text.
