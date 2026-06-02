# 0017 — Deterministic harp self-id marker for read-time recovery; defer the LLM-echo fallback

**Date:** 2026-06-01.

## Status

Accepted (the deterministic marker) and Deferred (the LLM-echo fallback).

## Context

`/recover` resolves "the session before the current one" at read time: `ListSessions` sorts the project's `.jsonl` transcripts by file mtime and `previousSessionByListing` returns `[1]`. This is purely datetime-positional and blind to *which harp owns which transcript*. When two terminals (two `ctxloom run` invocations, two harps) write transcripts into the same project directory, `[1]` can be the other terminal's session rather than this harp's previous one.

Read-time resolution therefore needs each transcript to declare which harp owns it, without depending on the session index, the binding, or the PID registry — all shown unreliable (see Seam S in the `meek-sunny-math` plan). The fix is a greppable marker the session emits into its own transcript at start:

	<ctxloom name="<harp>" kind="harp" />

ADR [0014](0014-remove-transcript-scan-binding.md) removed an earlier transcript marker, but a different one for a different purpose: that marker was injected into MCP `initialize` instructions (model-compliance dependent) and used to **string-match the harp name to bind `session_id`**. 0014's own revive trigger anticipated this work: "reintroduce a *deterministic* rescue — but bind it to a concrete recorded identifier, not a best-effort content scan." The marker here satisfies that condition — it is deterministic, structured, and namespaced (`kind="harp"`), and it serves **recovery resolution**, not binding.

The plan named two possible mechanisms: a deterministic marker written by the SessionStart injection path, and an LLM-echo instruction (via the always-loaded MCP harp instruction) as a fallback for backends without a reachable deterministic inject point — "biased to deterministic."

## Decision

Implement the deterministic marker and **defer the LLM-echo fallback**.

- The producer is `ctxloom session bind` — the universal committed SessionStart hook present in every ctxloom-launched session (regardless of whether a profile/inject-context is configured), which already reads `CTXLOOM_SESSION_HARP` and fires once per session including on `/clear`. It emits the marker as SessionStart `additionalContext`, which lands in the transcript (empirically confirmed: SessionStart `additionalContext` persists in the `.jsonl` as an `attachment.hook_success.stdout`, with `<` JSON-escaped to `<`).
- The consumer (`internal/harpmarker.Scan`) JSON-decodes the marker back out of that nested/escaped form; `ClaudeSessionHistory.harpFromTranscript` scans a transcript for it, and `previousSessionByListing` scopes to this-harp-marked transcripts when `CTXLOOM_SESSION_HARP` is set, falling back to the positional datetime `[1]` otherwise (pre-marker history, other backends).

The LLM-echo fallback is **not** built: `session bind` already covers every ctxloom-launched Claude session deterministically, so the echo would add model-compliance fragility (the exact failure mode 0014 removed) for no coverage gain today.

## Consequences

- Multi-terminal recovery is correct for Claude; single-terminal and historical (unmarked) sessions keep the prior datetime-positional behavior as a floor. No `SessionHistory` interface change — the harp is read from the environment.
- Gemini and other backends stay positional until their producer path is verified.
  (Resolved for Gemini in ADR [0023](0023-gemini-wire-format-conversions.md): its
  producer path was verified to HTML-escape the marker in-transcript, so Gemini
  scopes recovery on the session index instead — index-binding, not the marker.)
- The marker appears once in each session's context; it is a tiny self-closing tag and does not wrap content.

**Revive trigger (LLM-echo fallback):** a backend that needs harp-marker coverage but has no bind-equivalent deterministic SessionStart inject point. Then add the echo instruction for that backend only — keeping the deterministic path primary everywhere it is reachable.
