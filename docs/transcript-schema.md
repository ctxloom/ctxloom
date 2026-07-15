# ctxloom Canonical Transcript — Specification

**Status:** Slice S1 of 7 (`tough-cloud`) — **schema + Recorder shipped, capture
NOT wired yet.** This document and `internal/transcript/*` are the accepted
design; the four remaining consumer-facing pieces (host-side tee, canonical
reader, consumer cutover, scraper retirement) land in S2–S7. See "How to read
this document" below for what that means concretely.

**Contract version:** `v: 1` (see §6 — this is a schema new consumers will
build against; treat it as public once S2 starts writing it for real).

**Base:** v0.7.0-pre1, commit `33ef638`. Companion to the `tough-cloud` plan
(`~/.ctxloom/sessions/sixth-moist-kite/tough-cloud-transcript-capture.plan.md`),
which is the sequencing/rationale record. Where the two disagree on a shipped
fact, this document and the code win — the plan is upstream of the code, not
the other way around.

---

## 0. How to read this document

- **Unmarked = shipped in S1.** The schema (`Record` and its payload types),
  the JSON Schema, the fixtures, and `internal/transcript.Recorder` /
  `internal/transcript.Tee` exist in the tree and are covered by tests today.
- **Marked `> PLANNED (S2+)`** — designed, not yet built. Nothing in S1 wires
  a transcript into a live chat: no engine writes `transcript.acp.jsonl` yet,
  and no reader (`CanonicalHistory`) consumes one yet. `Tee` exists as a
  ready-to-call helper but is not called from `GRPCClient.Chat` or
  `coord/enginehost.adapt` — that's S2's job.

---

## 1. Why: ctxloom captures no transcript of its own

Every consumer of session memory today reads one of four private,
undocumented, version-unstable engine file formats through a per-engine
`agent.SessionHistory` implementation — and three of those four readers are
independently confirmed broken (codex: envelope-vs-flat parse; kiro: reads a
`v1` file while the real oneshot store is a `v2` sqlite; claude: recomputes the
wrong project-slug filename). The fix is not a fourth reader: it's to stop
scraping and capture the conversation at the one point ctxloom already holds
it in its own hands. Full rationale in the `tough-cloud` plan §1; this
document covers only the schema the capture writes.

---

## 2. Survey: what each engine actually exposes

The critical fact this schema leans on: **five of ctxloom's six engines
(codex, kiro, claude, opencode, and generic `acp`) drive structured chat
through the exact same code path** — `internal/acp.NewChatDriver` — which
normalizes every engine's wire protocol onto one Go type,
`agent.ChatEvent` (`internal/shared/agent/chat.go`), via
`internal/acp/mapping.go`'s `mapSessionUpdate`. Only **antigravity** has no
`StructuredChat` capability at all (no `internal/antigravity/chat.go`) — it
is oneshot/interactive-only, handled by a different regime (§5).

This is a stronger position than "pick a lowest common denominator": the
mapping in `mapSessionUpdate` is *already* a hand-tuned, tested, per-engine
union — codex, kiro, claude, and opencode each speak ACP's `session/update`
notifications (via `codex-acp`, `kiro-cli acp`, `claude-code-acp`, and
`opencode acp` respectively), and one file maps all four vocabularies onto one
Go type. The canonical transcript schema in §3 does not re-derive this union;
it wraps `agent.ChatEvent` in an envelope, verbatim.

### 2a. ACP `session/update` variants (the shared substrate)

| ACP wire variant | ctxloom `SessionEntryType` | Notes |
|---|---|---|
| `agent_message_chunk` | `assistant` | streamed assistant text |
| `agent_thought_chunk` | `thinking` | summarized reasoning — ACP surfaces this where claude's own stream-json strips it |
| `tool_call` | `tool_use` | title/kind → `ToolName`, rawInput → `ToolInput` |
| `tool_call_update` | `tool_result` | only once it carries output or a terminal status; `failed` → `IsError` |
| `plan` | `system` | rendered checklist; no dedicated plan type |
| `user_message_chunk` | *(dropped)* | never echo the user's own message back |
| `usage_update` / `session_info_update` *(out-of-SDK, hand-decoded)* | `ChatEvent.Complete` / `ChatEvent.Session` | the ONLY accounting data any ACP agent delivers — protocol v1 carries no token/cost/context-window/timing fields anywhere else |
| `session/request_permission` | `ChatEvent.Permission` | forwarded only under `ChatRequest.ForwardPermissions` |

### 2b. Native per-engine formats (for context / the `raw` escape hatch / S6 importers)

These are NOT what the Recorder reads (it reads `agent.ChatEvent`, already
normalized) — they matter for (a) explaining why the *old* per-engine readers
broke, and (b) informing the S6 one-shot importers that will convert
interactive-pty history into canonical form.

- **codex** (`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`): an *envelope*
  `{timestamp, type, payload}` where `type` is `session_meta` \|
  `event_msg` \| `response_item` \| `world_state` \| `turn_context`, and the
  conversational content sits inside `response_item.payload` (`type: message`,
  `role: developer|user|assistant`, `content: [{type: input_text|output_text,
  text}]`) or `response_item.payload.type: reasoning`. `event_msg.payload.type
  == "token_count"` carries the real accounting
  (`total_token_usage.{input_tokens,cached_input_tokens,output_tokens}`,
  `model_context_window`). The confirmed break (lived-zone) was a reader that
  assumed a flat record instead of unwrapping this envelope.
- **kiro** (`~/.kiro/sessions/cli/<id>.jsonl`, v1 — **the real oneshot store
  is a v2 sqlite `data.sqlite3`/`conversations_v2`, invisible to this file
  entirely**): `{version:"v1", kind: Prompt|AssistantMessage|ToolResults,
  data:{message_id, content:[{kind: text|toolUse|toolResult, data:...}]}}`.
- **claude** (`~/.claude/projects/<slug>/<uuid>.jsonl`, interactive TUI
  transcripts; claude-code-acp structured sessions do NOT write this file —
  they speak ACP on stdio instead): `{type: user|assistant|progress,
  message:{role, content:[{type: text|tool_use|tool_result,...}]}, sessionId,
  cwd,...}`. The confirmed break (tall-grab) was the reader's own
  cwd→directory-slug re-encoding landing on the wrong filename.
- **antigravity** (`~/.gemini/antigravity-cli/brain/<uuid>/.system_generated/
  logs/transcript_full.jsonl`): `{step_index, source: USER_EXPLICIT|SYSTEM|
  MODEL, type: USER_INPUT|CONVERSATION_HISTORY|PLANNER_RESPONSE, content,
  created_at, status}` — global store, keyed by an internal uuid the workDir
  cannot resolve (the tall-grab break).
- **opencode**: no private file scraped — its `SessionHistory` reads
  `opencode session list --format json` / `opencode export <id>`, opencode's
  own documented CLI surface. NOT broken, and NOT retired by this plan (memory
  "mimic-cli-native-surfaces"): kept as the correct fallback for
  interactive-opencode sessions even after canonical capture lands for its
  structured path.

### 2c. Integration principle

Where engines express the same concept under different names, this schema
maps to **one canonical field by meaning**, never by source label:

| Concept | codex | kiro | claude | antigravity (native) | Canonical |
|---|---|---|---|---|---|
| user turn | `response_item` role=`user` | `Prompt` | `message` role=`user` | `USER_INPUT` | `entry.type = "user"` |
| assistant text | `response_item` role=`assistant` / `event_msg.agent_message` | `AssistantMessage` text block | `message` role=`assistant` text block | `PLANNER_RESPONSE` | `entry.type = "assistant"` |
| model reasoning | `response_item.type=reasoning` (`summary`) | *(none observed)* | claude-code-acp: `agent_thought_chunk` | *(none)* | `entry.type = "thinking"` |
| tool invocation | `function_call` (not in the ACP stream — codex-acp maps this to `tool_call`) | `AssistantMessage` `toolUse` block | `tool_use` content block | *(none — oneshot only)* | `entry.type = "tool_use"` |
| tool output | `function_call_output` | `ToolResults` `toolResult` block | `tool_result` content block | *(none)* | `entry.type = "tool_result"` |
| turn accounting | `event_msg.token_count` | *(absent from v1 file)* | `usage` on the assistant message | *(none)* | `kind = "complete"` (`CompletePayload`) |

No new vocabulary is invented anywhere in this table — every canonical name on
the right is exactly `agent.SessionEntryType` or an `agent.ChatEvent` variant
name, already defined in `internal/shared/agent/{backend,chat}.go`.

---

## 3. The on-disk format

One JSON object per line at
`~/.ctxloom/sessions/<harp>/persist/transcript.acp.jsonl`
(`paths.HarpCanonicalTranscriptPath`). Append-only; a session that dies
mid-turn leaves a valid partial file (no trailing incomplete line, since each
`Record.Record` write is one complete marshaled line). Distinct from
`persist/transcripts/` (`paths.HarpTranscriptStoreDir`), which is an engine's
own native store bind-mounted for a containerized run — the two never
collide.

This is **authored session memory**, not derived cache: it lives under
`persist/` (survives workspace teardown, same rationale as the transcript
store) and is never gitignored.

### 3a. Envelope

```jsonc
{
  "v": 1,                        // schema version; unrecognized v = fail loud, never guess
  "harp": "sixth-moist-kite",    // ctxloom session id — the authoritative key
  "session_id": "019f6226-e5d2-75f3-b8bb-667866092679", // engine-native ACP session id (ChatEvent.Session.SessionID); "" until seen
  "engine": "codex",             // codex|kiro|claude|opencode|acp|antigravity
  "seq": 0,                      // monotonic per transcript, starting at 0, no gaps
  "ts": "2026-07-14T19:42:24Z",  // RFC3339 UTC — recorder RECEIPT time, not an engine timestamp
  "kind": "entry",               // entry|session|complete|permission — selects the payload below

  "entry": { … },       // present iff kind=="entry"
  "session": { … },     // present iff kind=="session"
  "complete": { … },    // present iff kind=="complete"
  "permission": { … },  // present iff kind=="permission"

  "raw": { … }           // OPTIONAL escape hatch — see §4
}
```

### 3b. Payloads

All four mirror an `agent.ChatEvent` variant field-for-field — see
`internal/transcript/record.go` for the authoritative Go types and
`docs/transcript.schema.json` for the machine-checkable shape.

- **`entry`** (`agent.SessionEntry`, minus `Timestamp` — the envelope's `ts`
  covers it): `type` (`user|assistant|thinking|tool_use|tool_result|system`),
  `content`, `tool_name`, `tool_input` (raw JSON), `tool_output`, `is_error`,
  `sidechain` (marks an engine's own in-harness subagent interior, e.g. a
  Claude Code Task sidechain — `agent.MainThreadEntries` is the filter that
  drops these for distillation/replay).
- **`session`** (`agent.ChatSessionInfo`, minus `SessionID` — hoisted to the
  envelope): `model`, `permission_mode`, `context_window`,
  `mcp_servers: [{name, status}]`.
- **`complete`** (`agent.TurnMeta`, carried in **FULL** — every field, not a
  trimmed subset, because this schema is a lossless superset): `input_tokens`,
  `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`,
  `context_window`, `max_output_tokens`, `cost_usd`, `model`, `stop_reason`,
  `duration_ms`, `num_turns`.
- **`permission`** (`agent.PermissionRequest`): `id`, `tool_name`,
  `tool_input`, `kind` (ACP `ToolCallKind`: execute\|edit\|delete\|move\|
  read\|search\|fetch\|think\|other — advisory, distinct from the envelope's
  `kind`), `options: [{id, kind, name}]`.

---

## 4. Fidelity: the `raw` field, and where this schema is honestly lossy

`agent.ChatEvent` is not a lossless copy of every engine's wire protocol — it
is already the mapping `mapSessionUpdate` chose to keep. Two concrete gaps:

1. `mapSessionUpdate`'s own documented drops: `user_message_chunk` (never
   echoed — would duplicate the user's own turn), unknown/malformed variants
   (dropped so the stream never crashes on a frame it doesn't model), and
   bare in-progress tool-call ticks with no output (status noise, not a
   result).
2. The two non-structured capture regimes (§5) are lossy by construction:
   oneshot capture has no tool granularity at all (prose only), and the
   interactive-pty importer (S6, not built yet) converts an engine's own file
   after the fact.

The `raw` field exists so a capture path that DOES hold the original frame —
an S6 importer reading a native engine file, or a future richer capture point
— can attach it without a schema version bump. **The `Recorder` in this slice
never populates `raw`**: `agent.ChatEvent` carries no such field to copy from.
This is a conscious, budgeted lossiness (plan §8, risk 3), not an oversight:
paying for full raw-frame retention on every line was decided against for S1;
revisit if a concrete consumer needs it.

---

## 5. Capture regimes (for context — S2/S6, not S1)

> PLANNED (S2+). Documented here because it explains the schema's `engine`
> enum including `antigravity`, which never reaches the tee.

- **Structured/ACP** (codex, kiro, claude-via-acp, opencode, generic acp):
  the tee at `GRPCClient.Chat` / `coord/enginehost.adapt` records every
  `ChatEvent` — zero scraping, full fidelity within `mapSessionUpdate`'s
  documented drops.
- **Oneshot** (antigravity `-p`, `kiro --no-interactive`, `codex exec`): no
  `ChatEvent` stream exists; capture a two-entry transcript (one `user` entry
  from the request prompt, one `assistant` entry from captured stdout).
- **Interactive pty**: cannot be teed at all — the engine's own TUI never
  crosses ctxloom's process. Handled by a one-shot importer converting the
  engine's native file to canonical form once, at session end (S6).

---

## 6. Schema evolution

`v` gates it. A `CanonicalHistory` reader (S3, not built yet) encountering a
`Record.V` it does not recognize must fail loud, per project discipline
(memory "isolation-must-not-negotiate": never silently mis-parse an unknown
shape). There is exactly one version today: `1`.

---

## 7. What this slice (S1) shipped, concretely

- `internal/transcript/record.go` — `Record` + `EntryPayload` /
  `SessionPayload` / `CompletePayload` / `PermissionPayload`, and the
  `agent.ChatEvent → Record` payload conversion.
- `internal/transcript/recorder.go` — `Recorder` (interface),
  `NewRecorder(harp, engine string) (Recorder, error)`, and `Tee(rec,
  events) <-chan agent.ChatEvent` (unwired — S2 calls this at its two host
  seams).
- `paths.HarpCanonicalTranscriptPath(harp)` — `internal/paths/paths.go`.
- `docs/transcript.schema.json` — machine-checkable JSON Schema for `Record`.
- `internal/transcript/testdata/fixtures/*.transcript.acp.jsonl` — one
  canonical-format fixture per engine (real captured content where available
  on this box; synthetic and labeled otherwise — see `MANIFEST.json` in the
  same directory for exactly which fields are real vs fabricated per file).
- Unit tests: schema/JSON-Schema conformance for every fixture, `Recorder`
  round-trip (real fixture content in → `Record` out, asserted on payload —
  not just entry counts), monotonic/gap-free `seq`, concurrent-append safety,
  and the empty-input contract (zero `Record` calls → no file written at all;
  see `recorder.go`'s `NewRecorder` doc comment for why this is a deliberate
  departure from an eager-open reading of the original design sketch).

Everything else — the tee actually wired into a live chat, the
`CanonicalHistory` reader, consumer cutover, scraper retirement, the
interactive-pty/oneshot capture code itself — is S2 through S7 and does not
exist yet.
