# 0014 — Remove the transcript-scan / scoped-instructions-marker session binding

**Date:** 2026-05-28.

## Status

Accepted (removal).

## Context

Binding a harp to its backend `session_id` originally had three layers, primary first:

1. **SessionStart hook** (`ctxloom session bind`) — fires once at session creation with `session_id` + `transcript_path` in the payload. The direct, designed-for-this path.
2. **Compact-time bind** — the compactor forward-binds if a session reaches compaction unbound.
3. **Transcript scan** (`discoverSessionByHarpName`) — a last-resort rescue in `ctxloom session distill` that iterated the backend's project-scoped `.jsonl` transcripts and string-matched the harp name in each entry's content/tool fields. To guarantee a hit, the harp name was injected into `ServerOptions.Instructions` on every MCP `initialize` (the "scoped instructions marker"), so it would be recorded in the transcript.

Layer 3 existed only because layer 1 was believed unreliable. Investigation showed layer 1 wasn't unreliable by design — it never fired for `ctxloom run` sessions because the Setup path that writes `settings.json` before launch never resolved bundle-shipped hooks, so `session bind` was simply absent from the config the backend loaded. Once that real bug was fixed (single shared hook assembler routed through by both writers — see `backends.AssembleManagedHooks`), the SessionStart hook fires reliably and was verified binding `session_id` + `transcript_path` live with no manual step.

That left the transcript scan as a fragile guess: it depended on string-matching content, on the marker being present, and on scanning unbounded transcript history — and it coupled the MCP server's `initialize` instructions to a binding side-effect. It guessed where the primary path now simply works.

## Decision

Remove layer 3 entirely: delete `discoverSessionByHarpName`, the harp-name marker emission in the MCP server, the `ServerOptions.Instructions` coupling, and the supporting tests. `ctxloom session distill <harp>` now **errors clearly** when the harp has no bound `session_id` instead of scanning to guess one. Binding is the two reliable layers: SessionStart hook (primary) + compact-time (backstop).

## Consequences

- A session whose SessionStart hook *and* compact-time bind both failed to fire is not auto-rescued; `distill` reports the missing binding rather than silently guessing. This is the correct failure mode — surface it rather than hide it behind a heuristic.
- The MCP server no longer injects the harp name into `initialize` instructions for binding purposes (it still names the session for the user).
- Less code, no transcript-history scan cost, no clock/marker fragility.

**Revive trigger:** if a real, recurring class of sessions ends up unbound through both reliable layers (e.g. a backend with no SessionStart hook support that also never compacts), reintroduce a *deterministic* rescue — but bind it to a concrete recorded identifier, not a best-effort content scan.
