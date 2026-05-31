# 0002 — Skip `ctxloom gc` command

**Date:** 2026-05-27.

## Status

Deferred.

## Context

Bundle reads route through a git clone cache under `.ctxloom/cache/repos/`. Each registered remote contributes a clone of its repo (with full history). Over time the cache grows monotonically; nothing prunes stale entries.

The original bundle-review plan proposed a `ctxloom gc` command for pruning the cache. Typical clones are tens to low hundreds of MB — projects with a handful of remotes sit comfortably under 1 GB. No user has reported the cache size as an issue.

## Decision

Don't implement `ctxloom gc`. Manual cleanup via `rm -rf .ctxloom/cache/repos/` is the documented escape valve.

## Consequences

The cache grows without bound; users have to know about the manual cleanup path. The clone cache is internal scratch space and the user-visible bundle behavior is unaffected by stale clones (just disk usage), so this isn't a correctness risk.

**Revive trigger:** a user issue mentioning cache size on disk, or own-dogfooding hits low-GB cache.
