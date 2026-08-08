# ctxloom Canonical Transcript — Specification

**Status:** SHIPPED, `tough-cloud` slices S1–S6 all landed on v0.7.0-pre1.
ctxloom captures its own transcript at the host-side `agent.ChatEvent` seams
for every structured/ACP engine, plus a low-fidelity two-entry capture for
oneshot `Execute` runs. `CanonicalHistory` is the live read path for
compaction, MCP memory tools, `session current|list|show|watch`, and the
resume picker. The four broken per-engine file scrapers (codex, kiro,
antigravity, claude) have been **deleted outright** — see §8. Interactive-pty
sessions (a human driving the engine's own TUI) are the one regime that
cannot be captured this way; that gap is scoped out for this release (§8,
task `petty-green`, post-pre1).

**Contract version:** `v: 1` (see §6). This is the schema every consumer
reads today — not a future target.

**Base:** v0.7.0-pre1. Companion to the `tough-cloud` plan
(`~/.ctxloom/sessions/sixth-moist-kite/tough-cloud-transcript-capture.plan.md`),
which is the sequencing/rationale record. Where the two disagree on a shipped
fact, this document and the code win — the plan is upstream of the code, not
the other way around.

---

## 0. How to read this document

Everything below describes shipped behavior. §5 and §8 mark the two known
gaps honestly (oneshot fidelity, interactive-pty), not as "not built yet" but
as accepted, deliberate scope limits for this release.

---

## 1. Why: ctxloom used to capture no transcript of its own

Before this schema, every consumer of session memory read one of four
private, undocumented, version-unstable engine file formats through a
per-engine `agent.SessionHistory` implementation — and three of those four
readers were independently confirmed broken (codex: envelope-vs-flat parse;
kiro: read a `v1` file while the real oneshot store was a `v2` sqlite;
claude: recomputed the wrong project-slug filename). The fix was not a
fourth reader: it was to stop scraping and capture the conversation at the
one point ctxloom already holds it in its own hands. Full rationale in the
`tough-cloud` plan §1; this document covers the schema the capture writes,
plus what actually shipped (§7–§8).

---

## 2. Survey: what each engine actually exposes

The critical fact this schema leans on: **five of ctxloom's six engines
(codex, kiro, claude, opencode, and generic `acp`) drive structured chat
through the exact same code path** — `internal/acp.NewChatDriver` — which
normalizes every engine's wire protocol onto one Go type,
`agent.ChatEvent` (`internal/shared/agent/chat.go`), via
`internal/acp/mapping.go`'s `mapSessionUpdate`. **antigravity** is the one
exception, but not because it lacks the capability: it has one
(`internal/antigravity/chat.go`) and implements it as a bespoke PROSE driver
over `agy -p` rather than through `NewChatDriver`, since agy has no ACP
subcommand and no first-party ACP adapter. Prose in, prose out — no
reasoning, tool-call or turn-accounting events — so antigravity contributes
nothing to the richer half of this schema through structured chat, and its
oneshot/interactive regimes (§5) are what actually feed it.

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
| `plan` | `system` | IR2 (2026-07): structured entries carried in `SessionEntry.Plan` (`SystemKind=="plan"`), not just a rendered checklist string — a re-emission (`ctxloom acp`) rebuilds a real ACP `plan` update from it instead of only a text fallback |
| `user_message_chunk` | *(dropped)* | never echo the user's own message back |
| `usage_update` / `session_info_update` *(out-of-SDK, hand-decoded)* | `ChatEvent.Complete` / `ChatEvent.Session` | the ONLY accounting data any ACP agent delivers — protocol v1 carries no token/cost/context-window/timing fields anywhere else |
| `session/request_permission` | `ChatEvent.Permission` | forwarded only under `ChatRequest.ForwardPermissions` |

### 2b. Native per-engine formats (historical context only)

These are NOT what the Recorder reads (it reads `agent.ChatEvent`, already
normalized), and as of S5 nothing in ctxloom reads them anymore either — the
readers below were deleted, not demoted to vendor readers (§8). Kept here only to
explain why the *old* per-engine readers broke, for anyone tracing history.

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
| session failure notice | *(none)* | *(none)* | *(none)* | `ERROR_MESSAGE` | `entry.type = "system"` (`SystemKindNotice`) |

No new vocabulary is invented anywhere in this table — every canonical name on
the right is exactly `agent.SessionEntryType` or an `agent.ChatEvent` variant
name, already defined in `internal/shared/agent/{backend,chat}.go`.

---

## 3. The on-disk format

One JSON object per line at
`~/.ctxloom/sessions/<harp>/persist/transcript.jsonl`
(`paths.HarpCanonicalTranscriptPath`). Append-only; a session that dies
mid-turn leaves a valid partial file (no trailing incomplete line, since each
`Record.Record` write is one complete marshaled line). Distinct from
`persist/transcripts/` (`paths.HarpTranscriptStoreDir`), which is an engine's
own native store bind-mounted for a containerized run — the two never
collide.

Named `transcript.jsonl`, not `transcript.acp.jsonl` (its name through
v0.7.0-pre1 up to the rename described here): the file is engine-agnostic by
construction (§2c) — fed by every structured/ACP engine, the oneshot regime,
AND the vendor readers (§8) — so a name suggesting it was ACP-specific was
misleading. Sessions captured before the rename have only the old filename
on disk; every reader resolves through
`paths.ResolveHarpCanonicalTranscriptPath`, which falls back to
`transcript.acp.jsonl` when the current name is absent, so pre-rename
sessions keep resolving without a migration step. Nothing writes the old
name again — the fallback is read-only.

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
  "kind": "entry",               // entry|session|complete|permission|raw — selects the payload below

  "entry": { … },       // present iff kind=="entry"
  "session": { … },     // present iff kind=="session"
  "complete": { … },    // present iff kind=="complete"
  "permission": { … },  // present iff kind=="permission"

  "raw": { … }           // OPTIONAL — see §4. Present iff kind=="raw" (the ONLY
                         // payload); may ALSO ride alongside "entry" as a `_meta`
                         // supplement, per the recorder's RawPolicy.
}
```

### 3b. Payloads

All four (plus the raw-only `kind:"raw"` line — §4) mirror an
`agent.ChatEvent` variant field-for-field — see `internal/transcript/record.go`
for the authoritative Go types and `docs/transcript.schema.json` for the
machine-checkable shape.

- **`entry`** (`agent.SessionEntry`, minus `Timestamp` — the envelope's `ts`
  covers it): `type` (`user|assistant|thinking|tool_use|tool_result|system`),
  `content`, `tool_name`, `tool_input` (raw JSON), `tool_output`, `is_error`,
  `sidechain` (marks an engine's own in-harness subagent interior, e.g. a
  Claude Code Task sidechain — `agent.MainThreadEntries` is the filter that
  drops these for distillation/replay). IR2 additions (2026-07, all optional):
  `tool_call_id` (engine-native tool-call id), `tool_kind` (ACP classification),
  `tool_locations`, `tool_content` (structured diff/terminal/content — mirrors
  ACP's `ToolCallContent` alongside the flattened `tool_output`),
  `content_blocks` (structured content alongside flattened `content`),
  `system_kind` (`""` notice \| `"plan"` — discriminates
  `EntryTypeSystem`'s two producers), `plan` (structured
  `[{content, priority, status}]` entries when `system_kind=="plan"`).
- **`session`** (`agent.ChatSessionInfo`, minus `SessionID` — hoisted to the
  envelope): `resumable` (the connected adapter advertised it can resume this
  native session by `session_id` on a later spawn), `model`,
  `permission_mode`, `context_window`, `mcp_servers: [{name, status}]`.
- **`complete`** (`agent.TurnMeta`, carried in **FULL** — every field, not a
  trimmed subset, because this schema is a lossless superset): `input_tokens`,
  `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`,
  `context_window`, `max_output_tokens`, `cost_usd`, `model`, `stop_reason`,
  `duration_ms`, `num_turns`.
- **`permission`** (`agent.PermissionRequest`): `id`, `tool_name`,
  `tool_input`, `kind` (ACP `ToolCallKind`: execute\|edit\|delete\|move\|
  read\|search\|fetch\|think\|other — advisory, distinct from the envelope's
  `kind`), `options: [{id, kind, name}]`, `tool_call_id` (IR2).

---

## 4. Fidelity: the `raw` field, and where this schema is honestly lossy

`agent.ChatEvent` is not a lossless copy of every engine's wire protocol — it
is already the mapping `mapSessionUpdate` chose to keep. Concrete gaps:

1. `mapSessionUpdate`'s own documented drops: `user_message_chunk` (never
   echoed — would duplicate the user's own turn), a true unknown/unmodeled
   variant with no `_meta` (dropped so the stream never crashes on a frame it
   doesn't model at all), and bare in-progress tool-call ticks with no output
   (status noise, not a result).
2. The oneshot capture regime (§5) is lossy by construction: no `ChatEvent`
   stream exists for a oneshot `Execute` run, so capture falls back to two
   entries (prompt + stdout) with no tool granularity at all.

**Update (IR3, 2026-07):** `agent.ChatEvent` now DOES carry a `Raw`
field — the protocol-level side channel for a curated allowlist
(`available_commands_update`, `current_mode_update`, any variant's `_meta`)
that has no dedicated IR projection of its own (see
`internal/acp/mapping.go`'s `rawOnlyEvent`/`metaRaw` and
`internal/acpagent/mapping.go`'s `rawOnlyUpdates`/`metaFromRaw`). The
Recorder in THIS package now populates `Record.Raw` FROM that field, gated by
a `RawPolicy` (`off | lossy-only | all`, default `lossy-only` —
`NewRecorder`'s `WithRawPolicy` option, `internal/transcript/recorder.go`):
`lossy-only` keeps `raw` only when it's a line's SOLE payload (a genuinely
otherwise-lost frame); `all` keeps it even alongside an already-captured
structured payload; `off` drops a raw-only line entirely rather than writing
an empty placeholder.

**This is a capture-layer decision, not a wire one.** `ChatEvent.Raw` already
exists (or doesn't) by the time any Recorder sees it — RawPolicy cannot make
the hub forward more than the ACP mapping layer chose to; it only decides
what of that gets written to DISK. It is UNRELATED to protocol `_meta`/
passthrough forwarding itself (which is unconditional and lives entirely in
`internal/acp`/`internal/acpagent`) — the two happen to share the word "raw"
and nothing else. **Permissions never ride this channel at all**, at either
layer: `session/request_permission` is not even a `session/update` variant,
so there is no `ChatEvent.Raw` producer that could carry one even in
principle.

Still not captured: a byte-for-byte copy of every OTHER frame (the fully
IR2-projected ones), and the interactive-pty importer that would have been
a second `raw` producer for the true unknown/malformed case (plan §4d option
A) — **not built**; the project instead deleted the per-engine readers
outright rather than demoting them (see §8).

---

## 5. Capture regimes (shipped)

Documented here because it explains the schema's `engine` enum including
`antigravity`, which never reaches the tee.

- **Structured/ACP** (codex, kiro, claude-via-acp, opencode, generic acp):
  the tee at `GRPCClient.Chat` (`internal/lm/grpc/chat.go`) and
  `coord/enginehost.adapt` (delegated children) records every `ChatEvent` —
  zero scraping, full fidelity within `mapSessionUpdate`'s documented drops.
  This is the default and the win: five of ctxloom's six engines run
  structured chat, so five of six get full-fidelity canonical memory with no
  per-engine parsing on the capture side at all.
- **Oneshot** (antigravity `-p`, `kiro --no-interactive`, `codex exec`): no
  `ChatEvent` stream exists; `transcript.RecordOneshot`
  (`internal/transcript/oneshot.go`) captures a two-entry transcript (one
  `user` entry from the request prompt, one `assistant` entry from captured
  stdout) at the runner's `Execute` seam.
- **Interactive pty**: cannot be teed at all — a human driving the engine's
  own TUI means the assistant's text never crosses ctxloom's process. **This
  is the one regime this release does not capture.** See §8 for what that
  means in practice and why.

---

## 6. Schema evolution

`v` gates it. `CanonicalHistory` (`internal/transcript/history.go`)
encountering a `Record.V` it does not recognize fails loud, per project
discipline (memory "isolation-must-not-negotiate": never silently mis-parse
an unknown shape). There is exactly one version today: `1`.

---

## 7. What's shipped, concretely

- `internal/transcript/record.go` — `Record` + `EntryPayload` /
  `SessionPayload` / `CompletePayload` / `PermissionPayload`, and the
  `agent.ChatEvent → Record` payload conversion.
- `internal/transcript/recorder.go` — `Recorder` (interface),
  `NewRecorder(harp, engine string) (Recorder, error)`, and `Tee`/
  `TeeAndClose(rec, events) <-chan agent.ChatEvent`, wired into
  `GRPCClient.Chat` (`internal/lm/grpc/chat.go`) and
  `coord/enginehost.adapt` (`internal/agentcoord/coord/enginehost.go`) — the
  two host-side seams that see every structured engine's event stream.
- `internal/transcript/oneshot.go` — `RecordOneshot`, the two-entry oneshot
  capture, wired at the runner's `Backend.Execute` stdout seam.
- `internal/transcript/history.go` — `CanonicalHistory`, the harp-keyed
  read view implementing both `agent.SessionHistory` and
  `pb.SessionSource`. It is now the live read path behind
  `internal/memory/compactor.go`, the MCP memory tools
  (`load_session`/`recover_session`/`get_previous_session`/
  `compact_session`), `ctxloom session current|list|show|watch`, and the
  resume picker.
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

---

## 8. Scraper removal and the interactive-pty gap

The four broken per-engine `SessionHistory` file readers named in §1 —
codex (`internal/codex`), kiro (`internal/kiro/session.go`), antigravity
(`internal/antigravity`), and claude (`internal/claude`) — have been
**deleted outright**. At the time of that removal (`tough-cloud` S5) they
were NOT demoted to a one-shot importer, as the plan's §4d option A had
originally recommended — that importer was a later, separate piece of work
(see the "Update" paragraph below, which supersedes this paragraph's
now-stale "no legacy read path left to fall back to" claim: the importer
package exists today). Each backend's `Backend.History()` now returns `nil`;
grep the removal commits for `tough-cloud S5` for the per-engine rationale
(e.g. codex: the envelope-vs-flat parse bug silently returned zero-entry
sessions; claude: the cwd→slug encoder resolved a directory that doesn't
exist for any workDir containing a dot, underscore, or space).
`TranscriptPathFromHook` is gone; `LocateTranscript`
(`internal/sessions/index.go`) and the `persist/transcripts/` bind-mount
discovery were NOT removed by S5 — they still resolve a harp's legacy
engine-native transcript by location (`fillTranscriptByLocation`) and back
`sessions.Entry.TranscriptPath`, which the vendor-reader locate step below
(codex/claude/antigravity) reads from. **opencode is explicitly excluded**
from the S5 removal: its native reader (`internal/opencode/capabilities.go`,
driving `opencode session list`/`opencode export`) was never broken — it
reads opencode's own documented CLI surface rather than a private file — and
stays wired as the fallback leg for interactive-opencode sessions (memory
"mimic-cli-native-surfaces").

The consequence, stated plainly (as of the scraper-removal commit): **a
session driven through an engine's own interactive TUI (not through
ctxloom's structured/ACP chat) has no ctxloom memory.** No canonical
transcript is captured for it (the tee cannot reach a pty), and — at that
point in time — there was no importer left to backfill one from the
engine's native file after the fact. This was a deliberate, honest scope cut
for v0.7.0-pre1 — not a silent regression — tracked as task `petty-green`
(interactive-session memory).

**Update (writer-a-wiring, closing petty-green's importer half):** the four
per-engine `vendorreader.VendorAdapter` implementations this section's own
successor work built (`internal/transcript/vendorreader/{codex,claude,
antigravity,kiro}`) are now WIRED IN, closing the gap described above for
the two moments that matter:

- **On exit of an interactive `ctxloom run`** (`internal/cli/run.go`'s
  `convertVendorTranscriptOnExit`, hooked right where `transcript.
  RecordOneshot` captures the oneshot-Execute regime): if the just-exited
  harp's backend has a resolvable vendor transcript and no canonical one yet,
  it is converted through the SAME `transcript.Recorder` sink the live tee
  writes through. Best-effort (a convert failure warns, never fails the
  run) and idempotent (a harp that already has a canonical transcript is
  never reconverted — see `operations.ConvertVendorTranscript`'s doc
  comment for why presence, not staleness, is the right check).
- **`ctxloom session backfill [<harp>]`** runs the identical conversion over
  already-indexed sessions — old sessions from before this wiring existed,
  or ones whose exit-seam import failed — instead of a just-exited one.
  Safe to re-run at any time for the same idempotency reason.

The engine→adapter+locate registry (`internal/operations/vendorreader.go`)
prefers each harp's already-bound `sessions.Entry.TranscriptPath` (the
SessionStart bind hook, or its PreToolUse-fallback equivalent for
antigravity) for codex/claude/antigravity — sidestepping the very cwd→slug
bug this section describes above. kiro is the one exception
(`vendorreader_kiro.go`): its bind, where one lands at all, is a
session_id, not a file path, so its locate falls back to
`kiroreader.EnumerateConversations` matched by project dir — a
best-effort heuristic, not a guarantee (two concurrent kiro-cli sessions in
the same project dir within the same window are indistinguishable by that
signal).

**opencode remains excluded** from all of the above, unchanged from this
section's original scope: it never had a broken reader to replace and keeps
its own native `opencode session list`/`opencode export` reader as the
interactive-session fallback.

Sessions indexed before this release, or created only through an
interactive-pty run, had no canonical transcript and nothing to distill,
recover, or browse via `ctxloom session`/the memory MCP tools — until the
backfill command above is run against them. `session backfill` (with no
harp argument) is the automatic backfill from the old per-engine files this
section originally said did not exist.
