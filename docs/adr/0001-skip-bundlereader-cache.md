# 0001 — Skip in-memory `BundleReader` read cache

**Date:** 2026-05-27.

## Status

Deferred.

## Context

`BundleReader` reads bundle bytes on demand: for each fragment / prompt fetch, it resolves the bundle's SHA from the lockfile and calls the cached fetcher (which itself memo-izes against the on-disk git clone cache). The original bundle-review plan listed an in-memory `(bundleName, sha, relPath) → bytes` cache layered on top of that, with the explicit guidance "add only if profiling shows it."

No measured slowdown has been reported.

## Decision

Don't implement the in-memory read cache.

## Consequences

`BundleReader.ReadFile` continues to round-trip through the cached fetcher every call. The cached fetcher's own memo handles repeated reads of the same SHA inside a single git-clone scope, which absorbs most of the would-be savings.

**Revive trigger:** a repeatable profile shows >50 ms cumulative per `assemble_context` call spent in `BundleReader.ReadFile`.
