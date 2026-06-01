# 0018 — `bundle edit` keeps add-only semantics; guards in place rather than routing through UpdateBundle

**Date:** 2026-06-01.

## Status

Superseded by [0019](0019-cli-pure-frontend.md).

The add-only-vs-upsert mismatch that justified keeping `bundle edit` out of
`UpdateBundle` was resolved by adding create-if-absent `AddFragments`/
`AddPrompts`/`AddMCPServers` fields to `UpdateBundleRequest`. `bundle edit` now
routes fully through `operations.UpdateBundle` (no in-CLI mutation, no separate
guard call). See 0019 for the CLI-pure-frontend invariant.

## Context

Seam B consolidates CLI write paths onto the `internal/operations` core so the CLI and MCP server share one implementation (symlink guard, re-distillation). Bundle `create` and `delete` route cleanly onto `operations.CreateBundle`/`DeleteBundle`.

Bundle `edit` does not map cleanly. The CLI `--add-fragment`/`--add-prompt`/`--add-mcp` flags are **add-only**: an item that already exists is left untouched and reported ("Fragment already exists: X"). `operations.UpdateBundle`'s `SetFragments`/`SetPrompts`/`SetMCPServers` are **upsert** (set semantics): they overwrite. Routing the add flags onto `Set*` would make `ctxloom bundle edit my-bundle --add-fragment existing` silently replace a fragment's real content with the flag path's placeholder text — data loss.

The CLI edit path also emits richer per-item feedback (duplicate-add and absent-remove info lines) than `UpdateBundle`'s change list.

The security requirement of Seam B is that the symlink-traversal guard (`requireSafeBundlePath`) extend to CLI writes. `bundle edit` mutates a loaded bundle in place and calls `bundle.Save()` directly, so before this change it had no guard.

## Decision

Keep `bundle edit`'s in-place mutation (`applyBundleEdits` and its add-only semantics + feedback) and close the security gap by calling the guard directly: export `operations.RequireSafeBundlePath` and invoke it in `runBundleEdit` before `bundle.Save()`. Route only `create` and `delete` — which have no semantic mismatch — through `operations.CreateBundle`/`DeleteBundle`.

## Consequences

- The symlink guard now covers all three CLI bundle write paths (create/delete via operations, edit via the exported guard), satisfying Seam B's security requirement.
- `bundle edit` retains its add-only behavior and per-item messages — no data-loss regression, no UX loss.
- One behavior the operations path offers is not gained by edit: re-distillation on content change. This is moot for the flag path, which only adds placeholder content (nothing meaningful to distill). Real-content edits happen via `$EDITOR` (`item_helpers.go`), addressed separately when fragment/prompt CRUD is consolidated.

**Revive trigger:** if `operations.UpdateBundle` grows add-only (create-if-absent) semantics for fragments/prompts/MCP, fold `runBundleEdit` onto it and drop the in-CLI mutation + the exported guard call.
