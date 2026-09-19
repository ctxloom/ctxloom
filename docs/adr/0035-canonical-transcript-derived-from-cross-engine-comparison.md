# 0035 — The canonical transcript's fields are derived from a cross-engine comparison, not invented

## Status

Accepted

## Context

This record preserves the comparison the canonical transcript format was
derived from. Several of the engines compared here were removed from the tree
after the decision was taken; they appear below because they ARE the
evidence, and a derivation with its inputs stripped out is no derivation.
`docs/transcript-schema.md` describes the resulting format as it stands
today and cites this ADR rather than repeating what follows.

### ctxloom captured no transcript of its own

At the time of the decision (v0.7.0-pre1, plan `tough-cloud`), every consumer
of session memory — compaction, the MCP memory tools, `ctxloom session`, the
resume picker — read one of four private, undocumented, version-unstable
engine file formats through a per-engine `agent.SessionHistory`
implementation. Three of the four readers were independently confirmed
broken:

- **codex** assumed a flat record where the real file is an envelope
  (`{timestamp, type, payload}`), so it silently returned zero-entry
  sessions.
- **kiro** read a `v1` jsonl file while the real oneshot store had moved to a
  `v2` sqlite database (`data.sqlite3`, table `conversations_v2`) the file
  never sees.
- **claude** recomputed the project-slug directory name from the cwd and
  landed on the wrong filename for any workDir containing a dot, underscore
  or space.

The fix was not a fourth reader. It was to stop scraping and capture the
conversation at the one point ctxloom already holds it in its own hands.

### What each engine actually exposed

Six engines were registered: codex, kiro, claude, opencode, a generic `acp`
backend, and antigravity.

**Five of the six drove structured chat through one code path.** codex, kiro,
claude, opencode and generic `acp` all spoke ACP's `session/update`
notifications (via `codex-acp`, `kiro-cli acp`, `claude-code-acp` and
`opencode acp` respectively), and one mapping — `internal/acp/mapping.go`'s
`mapSessionUpdate` — normalized all of those vocabularies onto a single Go
type, `agent.ChatEvent`. That mapping was already a hand-tuned, tested,
per-engine union.

**antigravity was the exception**, and not for lack of a chat capability: it
implemented one as a bespoke prose driver over `agy -p`, because agy had no
ACP subcommand and no first-party ACP adapter. Prose in, prose out — no
reasoning, tool-call or turn-accounting events. It therefore contributed
nothing to the richer half of the format through structured chat; its
oneshot and interactive regimes were what actually fed it.

#### The shared substrate: ACP `session/update` variants

| ACP wire variant | canonical `SessionEntryType` / event | Notes |
|---|---|---|
| `agent_message_chunk` | `assistant` | streamed assistant text |
| `agent_thought_chunk` | `thinking` | summarized reasoning — ACP surfaced this where claude's own stream-json stripped it |
| `tool_call` | `tool_use` | title/kind → `ToolName`, rawInput → `ToolInput` |
| `tool_call_update` | `tool_result` | only once it carries output or a terminal status; `failed` → `IsError` |
| `plan` | `system` | structured entries carried in `SessionEntry.Plan` (`SystemKind=="plan"`), not just a rendered checklist string, so a re-emission rebuilds a real `plan` update |
| `user_message_chunk` | *(dropped)* | never echo the user's own message back |
| `usage_update` / `session_info_update` *(out-of-SDK, hand-decoded)* | `ChatEvent.Complete` / `ChatEvent.Session` | the ONLY accounting data any ACP agent delivered — protocol v1 carried no token/cost/context-window/timing fields anywhere else |
| `session/request_permission` | `ChatEvent.Permission` | forwarded only under `ChatRequest.ForwardPermissions` |

#### The native per-engine files the old readers scraped

These were never what the recorder reads; they are recorded here because
they explain why the old readers broke and what each engine could express.

- **codex** (`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`): an envelope
  `{timestamp, type, payload}` where `type` is `session_meta` \| `event_msg`
  \| `response_item` \| `world_state` \| `turn_context`, with the
  conversational content inside `response_item.payload` (`type: message`,
  `role: developer|user|assistant`, `content: [{type: input_text|output_text,
  text}]`) or `response_item.payload.type: reasoning`.
  `event_msg.payload.type == "token_count"` carried the real accounting
  (`total_token_usage.{input_tokens,cached_input_tokens,output_tokens}`,
  `model_context_window`).
- **kiro** (`~/.kiro/sessions/cli/<id>.jsonl`, v1): `{version:"v1", kind:
  Prompt|AssistantMessage|ToolResults, data:{message_id, content:[{kind:
  text|toolUse|toolResult, data:...}]}}`. The real oneshot store was the v2
  sqlite database named above, invisible to this file entirely.
- **claude** (`~/.claude/projects/<slug>/<uuid>.jsonl`, the interactive TUI
  transcript; `claude-code-acp` structured sessions did NOT write this file —
  they spoke ACP on stdio): `{type: user|assistant|progress, message:{role,
  content:[{type: text|tool_use|tool_result,...}]}, sessionId, cwd,...}`.
- **antigravity** (`~/.gemini/antigravity-cli/brain/<uuid>/.system_generated/
  logs/transcript_full.jsonl`): `{step_index, source:
  USER_EXPLICIT|SYSTEM|MODEL, type:
  USER_INPUT|CONVERSATION_HISTORY|PLANNER_RESPONSE, content, created_at,
  status}` — a global store keyed by an internal uuid the workDir could not
  resolve.
- **opencode**: no private file was scraped. Its reader drove `opencode
  session list --format json` / `opencode export <id>`, opencode's own
  documented CLI surface, and was never broken.

#### The comparison the canonical fields were derived from

Where engines expressed the same concept under different names, one canonical
field was chosen **by meaning**, never by source label:

| Concept | codex | kiro | claude | antigravity (native) | Canonical |
|---|---|---|---|---|---|
| user turn | `response_item` role=`user` | `Prompt` | `message` role=`user` | `USER_INPUT` | `entry.type = "user"` |
| assistant text | `response_item` role=`assistant` / `event_msg.agent_message` | `AssistantMessage` text block | `message` role=`assistant` text block | `PLANNER_RESPONSE` | `entry.type = "assistant"` |
| model reasoning | `response_item.type=reasoning` (`summary`) | *(none observed)* | claude-code-acp: `agent_thought_chunk` | *(none)* | `entry.type = "thinking"` |
| tool invocation | `function_call` (not in the ACP stream — codex-acp mapped this to `tool_call`) | `AssistantMessage` `toolUse` block | `tool_use` content block | *(none — oneshot only)* | `entry.type = "tool_use"` |
| tool output | `function_call_output` | `ToolResults` `toolResult` block | `tool_result` content block | *(none)* | `entry.type = "tool_result"` |
| turn accounting | `event_msg.token_count` | *(absent from v1 file)* | `usage` on the assistant message | *(none)* | `kind = "complete"` (`CompletePayload`) |
| session failure notice | *(none)* | *(none)* | *(none)* | `ERROR_MESSAGE` | `entry.type = "system"` (`SystemKindNotice`) |

No new vocabulary was invented anywhere in this table: every canonical name
on the right is exactly an `agent.SessionEntryType` value or an
`agent.ChatEvent` variant name that already existed in
`internal/core/agent`.

## Decision

1. **Wrap `agent.ChatEvent` in an envelope, verbatim.** The canonical record
   does not re-derive the union above; it carries the already-normalized
   event, one JSON object per line, under a small envelope (`v`, `harp`,
   `session_id`, `engine`, `seq`, `ts`, `kind`) — see
   `docs/transcript-schema.md` for the shape as it stands.
2. **Map by meaning, not by source label.** The comparison table is the
   contract: a concept gets one canonical field regardless of what any engine
   calls it.
3. **The `engine` field carries the backend's registered name verbatim.**
   Nothing on the recording path normalizes, allowlists or refuses it — the
   registry (`internal/lm/backends`) is the vocabulary. This is the position
   that was later found to be violated by the published JSON Schema, which
   carried a short `claude` spelling that no writer ever emitted; the schema
   was corrected to the registered name rather than widened to admit both.
4. **Capture at the host-side seams, never by scraping.** The structured tee
   at the gRPC chat seam and the delegated-child engine host records every
   `ChatEvent`. A oneshot `Execute` run, which has no event stream, gets a
   deliberately low-fidelity two-entry capture (prompt + stdout).
5. **Delete the broken scrapers outright**, rather than demoting them to
   importers. The per-engine `SessionHistory` readers for codex, kiro,
   antigravity and claude were removed; each backend's `History()` returned
   nil. opencode's reader was explicitly excluded from the removal because it
   read a documented CLI surface and was never broken.
6. **Name the file `transcript.jsonl`, not `transcript.acp.jsonl`.** The
   format is ACP-shaped by lineage but engine-agnostic by construction, and a
   name implying ACP-specificity was misleading. Readers resolve the old name
   read-only for sessions captured before the rename; nothing writes it
   again.
7. **Accept the interactive-pty gap.** A human driving an engine's own TUI
   never routes the assistant's text through ctxloom's process, so the tee
   cannot reach it. This was scoped out of the release as a stated limit,
   not built around.

## Consequences

- **The format is ACP-shaped by lineage, not by dependency.** After the ACP
  packages, the generic `acp` backend, kiro, codex, opencode and antigravity
  were removed, the canonical record was unchanged: it wraps `agent.ChatEvent`,
  which outlived the protocol layer that first populated it. Nothing in the
  format requires any of the compared engines to exist.
- **The fidelity gaps are inherited from `mapSessionUpdate`'s own choices**,
  and remain: `user_message_chunk` is never echoed (it would duplicate the
  user's turn); a truly unmodeled variant with no `_meta` is dropped rather
  than crashing the stream; bare in-progress tool-call ticks with no output
  are dropped as status noise. The `raw` side channel and its `RawPolicy`
  were added later to keep otherwise-lost frames, and that is a capture-layer
  decision — it cannot make the mapping forward more than it chose to.
- **Permissions never ride the raw channel**, at either layer:
  `session/request_permission` was never a `session/update` variant, so no
  raw producer could carry one even in principle.
- **The interactive-pty gap was later closed for backends with a vendor
  reader**: a `vendorreader.VendorAdapter` converts the engine's native store
  through the same `transcript.Recorder` the live tee uses, on exit of an
  interactive run and on demand when a read finds no canonical transcript.
  The per-engine adapters for removed engines went with their engines. The
  claude cwd→slug bug named above is sidestepped rather than re-solved: the
  adapter prefers the transcript path the session-bind hook already resolved.
- **Fixtures named for removed engines were kept as provenance.** The real
  captures behind them cannot be remade, and the files are canonical-format
  JSONL that exercises the parser regardless of which engine produced the
  payload; `MANIFEST.json` beside them records which fields are real. A
  fixture's name is provenance, not a claim that a writer exists.
- **Historical transcripts on disk are still readable.** No reader validates
  the `engine` field at runtime, so a session captured from a removed engine
  loads exactly as it did.
