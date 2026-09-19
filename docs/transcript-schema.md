# ctxloom Canonical Transcript

ctxloom keeps its own record of every conversation it drives, in one format,
regardless of which engine drove it. This page describes that format as it
stands. The comparison of engine-native formats it was derived from — and why
each canonical field is the one it is — is
[ADR 0035](adr/0035-canonical-transcript-derived-from-cross-engine-comparison.md);
this page does not repeat it.

The machine-checkable shape is `docs/transcript.schema.json`; the Go types are
`internal/transcript/record.go`. Where this page and those disagree, they win.

---

## 1. Where it lives

One JSON object per line at
`~/.ctxloom/sessions/<harp>/persist/transcript.jsonl`
(`paths.HarpCanonicalTranscriptPath`). Append-only: each `Recorder.Record`
call writes one complete marshaled line, so a session that dies mid-turn
leaves a valid partial file with no trailing fragment.

It is **authored session memory**, not derived cache: it lives under
`persist/` (survives workspace teardown) and is never gitignored.

It is distinct from `persist/transcripts/` (`paths.HarpTranscriptStoreDir`),
which is an engine's own native store bind-mounted for a containerized run.
The two never collide.

Readers resolve the path through `paths.ResolveHarpCanonicalTranscriptPath`,
which also accepts `paths.LegacyCanonicalTranscriptFileName` read-only for
sessions captured under the file's earlier name. Nothing writes that name.

---

## 2. Envelope

```jsonc
{
  "v": 1,                        // schema version; an unrecognized v fails loud, never guesses
  "harp": "sixth-moist-kite",    // ctxloom session id — the authoritative key
  "session_id": "019f6226-…",    // engine-native session id (ChatEvent.Session.SessionID); "" until seen
  "engine": "claude-code",       // the driving backend's REGISTERED name, verbatim
  "seq": 0,                      // monotonic per transcript, starting at 0, no gaps
  "ts": "2026-07-14T19:42:24Z",  // RFC3339 UTC — recorder RECEIPT time, not an engine timestamp
  "kind": "entry",               // entry|session|complete|permission|raw — selects the payload

  "entry": { … },       // present iff kind=="entry"
  "session": { … },     // present iff kind=="session"
  "complete": { … },    // present iff kind=="complete"
  "permission": { … },  // present iff kind=="permission"
  "raw": { … }          // sole payload iff kind=="raw"; may also ride beside "entry" — see §4
}
```

**`engine` is the registered backend name** (`internal/lm/backends`), exactly
as `transcript.NewRecorder` received it. Nothing on the recording path
normalizes, allowlists or refuses the value; the registry is the vocabulary,
and the schema's `engine` enum is expected to admit every name the registry
can hand the recorder. `TestRecorder_EngineIsWrittenVerbatimAndValidatesAgainstThePublishedSchema`
pins that expectation for the shipped engine.

`session_id` is hoisted out of the session payload so any single line is
self-describing: once known, the recorder carries it onto every subsequent
line.

---

## 3. Payloads

Each payload mirrors one `agent.ChatEvent` variant field-for-field. The
canonical transcript does not define its own vocabulary: every entry type and
event kind is an `agent.SessionEntryType` or a `ChatEvent` variant that
already exists in `internal/core/agent`. That is the design decision ADR
0035 records.

- **`entry`** — `agent.SessionEntry`, minus `Timestamp` (the envelope's `ts`
  covers it). `type` is `user|assistant|thinking|tool_use|tool_result|system`.
  Alongside the flattened `content` / `tool_output` strings it carries the
  structured forms: `tool_call_id`, `tool_kind`, `tool_locations`,
  `tool_content`, `content_blocks`. `system_kind` discriminates the two
  producers of a `system` entry (`agent.SystemKindNotice`,
  `agent.SystemKindPlan`); a plan entry carries its structured items in
  `plan`. `sidechain` marks an engine's own in-harness subagent interior —
  `agent.MainThreadEntries` is the filter that drops those for distillation
  and replay.
- **`session`** — `agent.ChatSessionInfo`, minus `SessionID` (hoisted).
  `resumable` means the connected adapter advertised it can resume this
  native session by `session_id` on a later spawn.
- **`complete`** — `agent.TurnMeta`, carried in **full**. Every field, not a
  trimmed subset: the transcript is a lossless superset of what the engine
  reported.
- **`permission`** — `agent.PermissionRequest`. Its `kind` is the ACP
  tool-call classification and is advisory; it is distinct from the
  envelope's `kind`.

---

## 4. Fidelity, and the `raw` side channel

`agent.ChatEvent` is the mapping the engine adapter chose to keep, not a
byte-for-byte copy of the wire. Three drops are by design and are not
recoverable from the transcript: the user's own message chunk (it would
duplicate the user turn), an unmodeled variant carrying no `_meta`, and bare
in-progress tool-call ticks with no output.

`ChatEvent.Raw` is the side channel for frames that have no dedicated
projection. Whether it reaches disk is a **capture-layer** decision, governed
by `transcript.RawPolicy` (`transcript.WithRawPolicy` on `NewRecorder`):

- `RawLossyOnly` (`DefaultRawPolicy`) keeps `raw` only when it is a line's
  sole payload — a frame that would otherwise be lost entirely;
- `RawAll` keeps it beside an already-captured structured payload too;
- `RawOff` drops a raw-only line rather than writing an empty placeholder.

The policy cannot make the adapter forward more than it chose to; it only
decides what of that is written. Permissions never ride this channel — a
permission request is not a stream update, so nothing could produce one as
`Raw` even in principle.

---

## 5. Capture regimes

The transcript is written at ctxloom's own seams, never by reading an
engine's private files after the fact.

- **Structured chat.** The tee at `GRPCClient.Chat`
  (`internal/lm/grpc/chat.go`) and at the delegated-child engine host
  (`internal/core/coord/enginehost.go`) records every `ChatEvent`
  through `transcript.TeeAndClose`. Full fidelity within §4's drops.
- **Oneshot `Execute`.** No event stream exists, so
  `transcript.RecordOneshot` captures a two-entry transcript — one `user`
  entry from the request prompt, one `assistant` entry from captured stdout —
  at the runner's `Execute` seam. Deliberately low-fidelity: no tool
  granularity.
- **Interactive pty.** A human driving the engine's own TUI never routes the
  assistant's text through ctxloom's process, so nothing can be teed. For a
  backend whose descriptor declares a `vendorreader.VendorAdapter`
  (`engine.Descriptor.TranscriptReaders`), the engine's native store is converted
  through the same `transcript.Recorder` the tee uses — on exit of an
  interactive `ctxloom run` (`convertVendorTranscriptOnExit`), and on demand
  when a read finds no canonical transcript (`operations.ResolveAndHeal`,
  triggered by `transcript.NoCanonicalTranscriptError`). Conversion is
  best-effort and idempotent: a harp that already has a canonical transcript
  is never reconverted. A backend with no adapter, driven interactively, has
  no ctxloom memory.

---

## 6. Schema evolution

`v` gates it. `CanonicalHistory` (`internal/transcript/history.go`) fails loud
on a `Record.V` it does not recognize — never a silent mis-parse. The current
version is `transcript.SchemaVersion`.

---

## 7. Reading it back

`transcript.CanonicalHistory` is the harp-keyed read view, implementing both
`agent.SessionHistory` and `internal/lm/grpc`'s `SessionSource`. It is the live read path behind
compaction, the MCP memory tools and `ctxloom session`.

No reader validates a line against `docs/transcript.schema.json` at runtime;
the schema is enforced by the `internal/transcript` tests, which validate
every fixture and every recorder-written line against it.
