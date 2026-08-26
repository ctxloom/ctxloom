# Breaking changes in 0.7.0

**Read this before upgrading from 0.6.x.** ctxloom breaks rather than shims;
breaking *silently* is what it does not do — hence this page.

Twenty-five changes in this release are marked breaking. One of them fails
**silently** if you do nothing, so it is first.

---

## 1. Published bundle content now requires ctxloom 0.7 — and 0.6 does not say so

Bundle references moved to a canonical URI grammar:

    was   https://github.com/owner/repo@bundles/name#fragments/x
    now   ctxloom+git://github.com/owner/repo//bundles/name#fragments/x

The source class moved into the scheme, and `//` took over the repository/bundle
split that `@` used to carry. Both published bundle repositories now address
their content this way.

**No released ctxloom before 0.7 can parse it.** `internal/refuri` does not
exist at v0.6.4 and nothing there dispatches on the scheme.

**What that looks like if you upgrade the content but not the client.** Measured
with v0.7.0-19f6ada against the `ctxloom-personal` go-development closure:

| content | bundles resolved |
|---|---|
| pre-migration | 9 |
| migrated | 2 |

Seven bundles — `code-quality`, `conduct`, `containers`, `core-practices`,
`developer-mindset`, `go-ai-practices`, `just` — disappear with an **identical
exit code** and an identical `Pulled N items` line. Nothing reports a problem;
your assembled context is simply smaller.

Pins never move on their own, so this begins at `deps upgrade`, never at
install. **Upgrade the client first.**

Nothing lets a bundle declare a minimum client version today, which is why this
is silent rather than refused.

## 2. A config older than the current schema is refused, not repaired

Version-gated upgraders are gone. A `config.yaml` declaring a version below the
current one — or declaring none, which is the pre-versioning generation — now
fails with a migration finding naming the file, the version it declares, and
`ctxloom init` as the remedy. The pending-upgrade consent path remains as an
empty frame for future versions.

## 3. The config-level `hooks:` block is gone

It was a second implementation of something profiles already do, with exactly
one consumer and no writer anywhere. A config carrying `hooks:` now reports it
as an unknown key and ignores it.

**Move each hook to a profile** — `.ctxloom/profiles/<name>.yaml`, under the same
`hooks:` key, spelled identically. Note the trust consequence: a profile's
directly-declared hooks pass the executable trust gate, which the config block's
did not.

## 4. Session essences moved under the harp

A per-rotation essence now lives at
`~/.ctxloom/sessions/<harp>/segments/<sessionID>.md`, beside that rotation's
canonical `<sessionID>.jsonl`. The project-rooted
`<project>/.ctxloom/sessions/<sessionID>.md` store is no longer written or read.
Existing files there are not migrated and are safe to delete; `ctxloom session
adopt` re-indexes an outside session if you want its history back.

## 5. A hook declared once runs once, however many profiles reach it

The same hook no longer runs twice in one event. Previously, a hook declared by
a profile that two of your profiles both inherit from was applied once per
inheritance path — a shared ancestor reached through two parents ran its hook
twice per event. Two selected profiles each declaring the same hook did the
same.

Hooks are compared on their whole executable content — type, command, prompt and
matcher — **scoped to the event**. So the same command registered on both
`session_start` and `session_end` is still two hooks, and the same command with
a different `matcher` is still two hooks. Only an exact repeat within one event
collapses.

**If you were relying on a hook running twice, it will now run once.** That was
not expressible on purpose and there is no way to ask for it; declare two hooks
that differ in some way if you need two runs.

## 6. `profiles.defaults` was replaced by an always-bound default agent

Set `default_agent: <name>` and `agents.<name>.profiles: [...]`. A config still
carrying `profiles.defaults` is told so by name.

## 7. A session started before 0.7 will not have its transcript converted

Every session already on your disk was started before ctxloom recorded which
engine version was running. Reading an engine's own transcript store back is
now scoped to that version, so those sessions **refuse to convert** rather than
being read by a parser nobody validated against them. That refusal is the
designed outcome, not a fault — this section is here so you can recognise it
and know there is nothing to fix.

**What you will see.** Nothing fails and nothing is deleted. You get a warning,
at the end of an interactive `ctxloom run` and when you `/recover` a session
(the `recover_session` tool), naming the session and the reason:

    ctxloom: warning: vendor transcript import: fluent-amber-heron records no
    claude-code version, so ctxloom cannot tell which transcript format wrote
    it (sessions started before ctxloom recorded engine versions, or whose
    engine could not be asked, carry none) — refusing to read rather than
    guessing at a format

A session whose version *is* recorded but falls outside every range this build
carries a reader for gets the other refusal, which prints the gap:

    no codex transcript reader is validated for version "0.150.0" (ctxloom
    carries: 0.144.0 – 0.145.0 (exclusive)) — refusing to read rather than
    parsing a format it has never been checked against

**What it costs you.** Only ctxloom's own canonical transcript for that
session, and therefore anything downstream of it — a distilled essence, a
`/recover` that replays the conversation. The engine's own transcript is
untouched on disk in the engine's own store, and the engine's own resume still
works. Nothing is lost; it is not re-stated in ctxloom's format.

**Why it refuses instead of trying.** A reader that guesses does not fail
loudly — it produces a transcript that looks completely fine, is wrong, and
goes straight into a model's context. That is the failure this project has
already been burned by (the per-engine history scrapers deleted in 0.7 were
removed for exactly these mis-parses). A refusal you can read is recoverable; a
plausible fabrication is not.

**There is deliberately no backfill and no re-record**, and both of the obvious
paths are worse than the refusal:

- *Re-running the session to record a version* would record what is installed
  **now**, which says nothing about what wrote bytes from months ago. It
  produces a confident, wrong answer — the exact thing being avoided.
- *A one-off backfill command* would have to pick a reader for a session whose
  format is unknown. The only available guess is "the newest one", and the
  newest reader is the one most likely to be wrong about the oldest session.

**Going forward.** Sessions started under 0.7 record the engine version at
session start and convert normally. Two things can still leave a 0.7 session
unreadable, and `ctxloom doctor` reports both before they bite, on its
`DOCTOR-CHECK-TRANSCRIPT-READER-v2` line: an engine whose version could not be
probed at all (that session records nothing, and will refuse just like a 0.6
one), and an installed engine version outside every range this build carries a
reader for. That line names the version detected on your machine, the reader it
selects, and every range ctxloom carries — which is also the bug report that
gets a reader written for a version that has none.

## 8. Everything else marked breaking

Grouped by what you would have to change.

**CLI surface**
- `session delete` actually destroys the session (it previously did not).
- `session purge` fans out to the population that owns each destroyer.
- `session backfill` is deleted, and nothing replaces it — see §7 for what that
  means for the sessions you already have.
- The transcript has its own sub-noun.
- A session is renamed by assigning its name, not by a verb.

**Configuration and content**
- MCP servers come from bundles only; ctxloom's own ships as a builtin.
- `select_tags` (selects content) is split from `tags` (descriptive only). A
  profile relying on `tags` to select content selects nothing until updated.
- `Hook.Order` and the hook sidecar are dropped.
- `subagent` is renamed to `agent` across config, CLI, API and prompts.

**Isolation**
- The container runtime axis splits into two ownership modes,
  `container-rootless` and `container-rootful`. There is deliberately no "any
  container" value, and an ownership mismatch is fatal rather than a
  substitution.
- A requested container that cannot start is fatal unless `--degraded`, which
  falls back to the HOST and never to the other ownership mode.
- Container identity contracts are enforced and ownership residue is surfaced.

**Trust**
- A `distill` prompt the trust gate withholds now REFUSES the run — `bundle
  distill`, `fragment distill`, `command distill` and item edits exit 2 and name
  the item — instead of silently distilling with ctxloom's built-in default. A
  project that never configured a `distill` prompt is unaffected: absence still
  falls back to the default, because absence is not a decision.
- A local attestation overrides a broken or absent remote signature.
- Countersignatures bind a composite attestation form, not a kind label.
- The pending-lockfile review ceremony and blind mode are gone.

**Backends**
- The `gemini` backend is replaced by `antigravity`.
- `taskloom` and `ltk` ship as bundled companions.

**ACP**
- `fs/read_text_file` and `fs/write_text_file` are confined to the session
  workspace.
