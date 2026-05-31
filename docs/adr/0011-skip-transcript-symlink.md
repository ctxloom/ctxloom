# 0011 — Skip transcript symlink in harp directory

**Date:** 2026-05-27.

## Status

Deferred.

## Context

The original sessions plan proposed dropping a symlink in `<harp-dir>/transcript.jsonl` pointing at the backend's actual session transcript path. The intent was snapshot-fidelity convenience: a future debugger or distill run could open the harp dir and find the raw transcript next to `essence.md`.

`backends.Session` doesn't carry a `Path` field today. Adding one across every backend implementation (claude-code, codex, gemini, plus the mock used in tests) for symlink emission is a meaningful API surface change.

`essence.md` — the compactor's output — is the actual resume source. The raw transcript is rarely needed at the harp path.

## Decision

Don't add the symlink. `essence.md` remains the canonical resume surface.

## Consequences

To get to the raw transcript, a user or tool has to look up the backend-native session path (e.g., `~/.claude/projects/<proj>/<sid>.jsonl`) rather than just following a harp-dir symlink. That's an extra hop but doable.

**Revive trigger:** a user workflow needs the raw transcript at the harp path (not just essence.md) and the indirection becomes load-bearing friction.
