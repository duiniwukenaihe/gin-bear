# ADR-002: budget metering semantics

Date: 2026-09-20. Status: accepted for M3 experimental use only.

## Metered spend

The ledger counts exactly two things: vendor-reported tokens and a
conservative per-turn reserve for silent turns. Nothing else is spend.

## Pre-call gating

Before each model call the runner refuses when remaining budget cannot cover
the estimated input (chars/4 heuristic) plus one minimal turn, and it passes
the remaining output allowance as `max_tokens`. Vendor output is therefore
hard-capped per turn; a second, quieter bound comes from `MaxOutputTokens`.

## What is not claimed

Input metering is best-effort: no hard vendor-side input guarantee is
asserted, and silence from a vendor never counts as zero. When metering is
impossible (no usage channel at all), the runner still refuses on reserves
but operators must not present that as a hard cost ceiling.
