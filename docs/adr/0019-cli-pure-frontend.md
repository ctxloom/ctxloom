# 0019 — The CLI (and every frontend) is a pure frontend over internal/operations

**Date:** 2026-06-01.

## Status

Accepted.

## Context

Through Seam B the CLI and MCP server increasingly delegated domain writes to `internal/operations`, but pockets of domain logic remained in `cmd/`: direct bundle/profile file I/O, loader `.Save()`/`.Delete()` calls, raw domain YAML, and business decisions (default promotion, parent validation, add-only semantics). The same drift that motivated the seam — two implementations of one operation — recurs whenever a frontend does the work itself instead of calling the shared core.

ADR [0018](0018-bundle-edit-keeps-add-only-semantics.md) carved out one exception: `bundle edit` kept its in-CLI mutation because its add-only flag semantics couldn't map onto `UpdateBundle`'s upsert `SetFragments`.

## Decision

Adopt a single invariant: **a frontend (CLI, MCP, future) parses input, calls `internal/operations`, and renders output — it does no domain logic itself.** Frontend-only concerns remain in the frontend: argument parsing, printing, `$EDITOR`, TTY detection, stdin confirms, constructing requests, and building the `operations.Distiller`. Everything that reads or mutates domain state (bundles, profiles, fragments, prompts, remotes, config, sessions) goes through operations — reads included, so operations is the sole component that touches domain files.

To remove the 0018 exception, `UpdateBundleRequest` gained create-if-absent `AddFragments`/`AddPrompts`/`AddMCPServers` (the add-only counterpart of the upsert `Set*`). `bundle edit` now builds an `UpdateBundleRequest` and calls `UpdateBundle` — no in-CLI bundle mutation, no separate guard call.

## Consequences

- One implementation per operation, one place for the symlink guard / validation / distillation, and immediate reusability across frontends (an MCP `add_fragment` tool calls the same `operations.AddItem` the CLI does).
- Frontends lose some bespoke per-item feedback (e.g. "fragment already exists") in favor of the operations change list — an accepted trade already made for profiles and bundles.
- The remaining violations found in the 2026-06-01 cmd/ audit are tracked as tasks and worked down against this invariant (export/import cores, default-setters, read-path cores, MCP-handler mutations, `init` bootstrap).

**Revive trigger:** none — this is the standing rule. A new frontend that needs an operation builds (or extends) an `operations` function rather than implementing it locally.
