# 0004 — Skip "Review each one at a time" UX loop

**Date:** 2026-05-27.

## Status

Deferred.

## Context

The original bundle-review plan included an `R` reply option that would step through pending bundles one at a time, re-presenting the review template with a ✓ marker for those already reviewed. The intent was to reduce cognitive load when many bundles are pending review.

The actual template (2026-05-27) already supports per-bundle action via `S <name>` (show), `D <name>` (decline one), `P <name>` (pin one), plus `T <remote>` (trust whole remote). A user can walk through bundles individually without a stateful "review loop."

## Decision

Don't implement the `R` flow. The existing per-name shortcuts cover the same outcomes without adding state (which bundles have been seen) or a re-presentation cycle.

## Consequences

The review template stays static: same content on every blocked-tool turn until pending clears. For reviews with many bundles, the user re-reads the full list each turn — slight friction compared to a stepping flow.

**Revive trigger:** a user has >5 bundles in a single review *and* reports the per-name commands as too tedious in practice.
