# 0012 — Skip `harp-go` extraction

**Date:** 2026-05-27.

## Status

Deferred.

## Context

`internal/harp/` is a Go port of harp-core: deterministic, human-readable ID generation for sessions ("swift-amber-falcon"). The plan called out promoting it to a standalone repo at `github.com/benjaminabbitt/harp-go` for independent versioning and external reuse.

The API is small but still evolving. No external consumer has asked to vendor it. Extracting now would couple ctxloom to a separate release cadence and a module boundary that's currently free to move.

## Decision

Keep `internal/harp/` internal. Don't extract.

## Consequences

External Go consumers can't import harp from ctxloom. The API stays free to refactor without semver implications.

**Revive trigger:** ANY of —
- A non-ctxloom consumer wants to vendor harp.
- The API stabilizes (no signature changes for a sustained period).
- We want versioned harp releases independent of ctxloom (e.g., to fix a harp bug without cutting a ctxloom release).
