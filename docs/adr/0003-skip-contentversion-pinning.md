# 0003 — Skip `Reference.ContentVersion` pin promotion

**Date:** 2026-05-27.

## Status

Superseded.

**Superseded by:** the 2026-05-27 implementation of `pin_bundle` / `unpin_bundle` MCP tools, which set a `Pinned` flag on the active `LockEntry`. See `cmd/mcp_tools_review.go`, `internal/remote/types.go::LockEntry.Pinned`, `internal/operations/lockfile_pending.go::SetBundlePin`.

## Context

The original bundle-review plan proposed "promote repeated session declines into permanent `Reference.ContentVersion` pins in config." The goal was a user-facing escape from review noise: "stop asking me about this bundle every session."

The proposed mechanism — rewriting profile YAML refs from `alice/foo` to `alice/foo@<sha>` — would have touched user-authored files and required schema/parser changes throughout the profile pipeline.

## Decision

Achieve the same user-facing goal a different way: add a `Pinned bool` to the lockfile entry. `DiffLockfiles` skips active entries with `Pinned=true` so the review template stops surfacing them. Pin is set via `pin_bundle` and cleared via `unpin_bundle`.

## Consequences

- Lockfile-side pin is reversible in one command (`unpin_bundle`).
- The pin is localized to a single file (`lock.yaml`) instead of every profile that references the bundle.
- Profile schema and `Reference.ContentVersion` parsing stay unchanged.
- **Trade-off:** because the lockfile is typically gitignored, the pin doesn't travel with the project across machines / `git clone`. If that matters later, this decision gets revisited.

**Revive trigger:** a user wants their `pin_bundle` choice to survive `git clone` of the project across machines — at which point the pin needs to move from the lockfile (gitignored) into the profile (committed).
