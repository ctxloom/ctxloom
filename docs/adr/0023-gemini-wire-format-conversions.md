# 0023 — Gemini adapter owns its wire-format conversions; recovery binds on the index

**Date:** 2026-06-02.

## Status

Accepted.

## Context

The Gemini backend was scaffolded to the `Backend` interface but never verified
against a real Gemini CLI. Every load-bearing detail was written against Claude's
shapes or a fictional Gemini schema, so none of the integration actually worked.
Verified live against Gemini CLI 0.44.1:

- **Transcript store layout.** Gemini writes `~/.gemini/tmp/<slug>/chats/` where
  `<slug>` is a slugified project basename, with the real absolute path in a
  sibling `.project_root` file. The adapter computed `~/.gemini/tmp/<sha256(path)>`,
  an abandoned pre-March layout, so it could not locate current sessions.
- **Transcript format.** The `transcript_path` from the hook payload is JSONL: a
  header line (`{sessionId, projectHash, …}`) then one message per line
  (`{id, timestamp, type, content}`), `type ∈ {user, gemini, info, error}`, with
  user `content` a `[{"text": …}]` array and other turns a plain string. The
  adapter parsed the whole file as one `{messages:[{role,…}]}` object — wrong
  extension, wrong parse strategy, wrong field — yielding empty sessions.
- **Hook registration schema.** Gemini requires the nested shape
  `event → [{matcher, hooks:[{type:"command", command, timeout(ms)}]}]`. The
  adapter emitted Claude's flat `[{command}]`, which Gemini silently ignores, so
  context injection and `session bind` never fired. Gemini timeouts are
  milliseconds, not seconds.
- **Workspace trust.** In an untrusted folder Gemini refuses to run headless and
  will not execute project hooks; `GEMINI_CLI_TRUST_WORKSPACE=true` lifts both.
- **Harp marker.** ADR [0017](0017-harp-self-id-marker.md) deferred Gemini harp
  scoping until its "producer path is verified." It now is: Gemini injects
  SessionStart `additionalContext` into the first user turn but HTML-escapes it
  (`&lt;ctxloom …`), so a literal-`<` marker scan can't recover it. However, the
  SessionStart hook payload carries `session_id` and `transcript_path` with the
  same field names Claude uses.

## Decision

Keep the `Backend` interface and the normalized `Session`/`SessionEntry` types as
the polymorphic contract, and make the **Gemini adapter own every conversion** —
both parsing Gemini's real formats into the contract and emitting artifacts valid
for Gemini. No Gemini shapes leak into shared code; no Claude shapes leak into the
adapter.

- Resolve the project dir by scanning `~/.gemini/tmp/*/.project_root` for the
  absolute path (sha256 lookup kept only as a legacy fallback).
- Parse JSONL line-by-line into normalized entries, decoding the polymorphic
  `content`; keep a legacy whole-file `{messages:[…]}` path and raw-string
  fallbacks so schema drift degrades to a partial session, never an error.
- Emit hooks in Gemini's nested schema with `type:"command"`, timeouts converted
  seconds→ms, and a durable `name:"ctxloom-managed"` marker for clean removal.
- Set `GEMINI_CLI_TRUST_WORKSPACE=true` in the child env from inside the adapter's
  `Execute`, so a ctxloom-launched project starts headless and fires hooks.
- Resolve harp recovery from the **session index**, not transcript content:
  `ctxloom session bind` records the active harp's `session_id`/`transcript_path`
  (both in Gemini's payload), and `GetPreviousSession` returns the most recent
  prior index entry's transcript, falling back to the positional datetime floor.
  Look up `GetSession` by the header `sessionId` since Gemini filenames are not
  the UUID. The ADR 0017 LLM-echo fallback is **not** built — index-binding
  supersedes it for Gemini.

Shared config no longer hands a Claude-specific compaction model (`haiku`) to a
non-Claude backend: `GetCompactionModel` returns empty for non-Claude compaction
LLMs so the backend resolves its own default.

## Consequences

- Gemini session reassembly, harp-scoped multi-terminal recovery, context
  injection, `session bind`, distillation, and per-LLM config now work, verified
  end-to-end (hooks fire; bind records `session_id`+`transcript_path`; a real
  `.jsonl` reassembles to its turns; distill completes with a valid model).
- ADR 0017's revive trigger for Gemini is resolved by index-binding rather than
  the deferred LLM-echo; the marker remains Claude-only.
- Gemini's tmp layout and transcript format have already changed twice across
  versions; the adapter pins the current schema in tests and keeps legacy +
  raw-string fallbacks so future drift degrades gracefully.

**Revive trigger:** a future Gemini release that drops `.project_root`, changes
the JSONL header/message schema, or alters the SessionStart payload fields
(`session_id`/`transcript_path`). Re-verify against a captured live transcript and
payload before adjusting the adapter.
