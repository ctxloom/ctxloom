# 0013 — Keep `ctxloom-default-tasks` embedded

**Date:** 2026-05-27.

## Status

Deferred.

## Context

The tasks workflow ships via a built-in bundle at `resources/builtin_bundles/tasks.yaml`, embedded into the binary at build time. The canonical home for the bundle is `github.com/ctxloom/ctxloom-default`, and a standalone YAML at `examples/bundles/tasks.yaml` is provided for users who want to inspect or fork it.

Making the bundle optional (an opt-in feature flag) would require: a flag in config, plumbing into `resolveBuiltinBundleHooks` to consult it, and a UX decision about whether new installations default the flag on or off. Today the bundle is always-on; everyone gets task auto-capture and plan-stamping out of the box.

## Decision

Keep the tasks bundle embedded and always-on. Don't add a feature flag to disable it.

## Consequences

Users who want a fundamentally different task workflow (or no task workflow at all) can't easily disable ours — they'd have to fork or modify the binary. The flip side: zero-config task UX for everyone else.

**Revive trigger:** a user wants the tasks workflow turned off entirely (not just modified) — at which point a feature flag becomes worth the surface-area cost.
