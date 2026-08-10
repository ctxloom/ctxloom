---
title: "Session Memory"
---

You hit a context limit, or you just want a clean slate mid-task. Normally that means `/clear` and then rebuilding your bearings by hand: scrolling back through the old conversation, copy-pasting chunks of it into the fresh one, guessing which decisions and code and half-finished threads still matter, fighting the token limit as you paste. You grab too much or miss the one detail that mattered, and the formatting doesn't survive the trip. It's salvage work, not the work you meant to be doing.

With ctxloom, that ritual is two commands:

```
/clear
/recover
```

`/clear` empties the window but doesn't end the session — the same session keeps growing underneath it. `/recover` re-reads that session's still-growing transcript from disk and hands back a fresh distillation of it. Your bearings come back on their own — no scrolling through the old transcript, no guessing what to save.

## Why ctxloom captures its own transcript

Earlier versions of ctxloom didn't record a conversation at all — they went looking for it afterward, in each engine's own private, undocumented session file (a Claude Code JSONL under `~/.claude/projects/...`, a Codex rollout file, a Kiro sqlite store, and so on), and re-derived the pieces ctxloom needed: which file belongs to which project, how to decode that engine's particular record shape. That worked until it didn't: a wrong filename, an unhandled record shape, or a store format ctxloom's reader didn't know about, and recovery silently came back with nothing — no error, no warning, just an empty distillation for a session that plainly wasn't empty. `/recover` above only works if there's actually something to recover.

ctxloom no longer goes looking. For every engine driven through ctxloom's structured chat path, the conversation already flows through ctxloom's own process as it happens — every message, tool call, and tool result crosses ctxloom on its way between you and the model. ctxloom now captures that stream itself into one canonical, engine-agnostic transcript per session, instead of trying to reconstruct it later from a file it doesn't control. There is one format, one reader, and no per-engine guessing. See [docs/transcript-schema.md](https://github.com/ctxloom/ctxloom/blob/main/docs/transcript-schema.md) for the on-disk schema, if you want the detail.

Two capture regimes cover ctxloom's structured usage:

- **Structured / chat sessions** (the default for `ctxloom run` across codex, kiro, Claude Code, opencode, and generic ACP agents) get full-fidelity capture: every user turn, assistant message, reasoning step, tool call, and tool result, in order.
- **Oneshot runs** (`ctxloom run --one-shot`, `codex exec`-style one-off prompts) don't stream turn-by-turn, so capture is lower-fidelity: just the prompt you sent and the reply that came back. No tool-by-tool detail, but never nothing — a oneshot run that used to leave no memory behind now leaves at least the shape of what happened.

The one gap this release doesn't close: if you drive an engine's own interactive terminal UI directly rather than through ctxloom's structured chat, the conversation never crosses ctxloom's process, so there's nothing for ctxloom to capture — the old per-engine file scrapers that used to (unreliably) cover this case have been removed, not replaced. That path has no ctxloom memory for now; it's tracked for a future release. Everything else below assumes the structured or oneshot path, which is how `ctxloom run` operates by default.

## Why this runs out of band

Your harness's own compaction (`/compact` or its auto-compact equivalent) is the right tool for live context pressure, and ctxloom doesn't compete with it — use it when your current session is getting full.

Session memory solves a different problem: a summary that outlives the session. In-context compaction asks the agent to summarize itself at the exact moment it has the least room to think. A context-starved agent writes a starved summary, and whatever it drops on the way out is gone for good — there's no going back for it after `/clear`.

ctxloom distills out of band instead. It reads the complete transcript from disk, in a fresh process, with a full budget, using a separate fast model (default: Haiku). The summary gets written where it actually has room to be good, and it's saved to disk, so it's still there after `/clear` — or after the process restarts, or a day later.

Distillation is **on-demand**. Nothing distills a session automatically when it
ends — a session stays title-less until something explicitly asks for its essence.

It is triggered for you in two places:

- On resume: `ctxloom run --session <harp> --distill` distills the previous session
  first, so its essence is ready to inject into the new run.
- On recovery: `/recover` (or asking "what were we working on?") distills on demand,
  which is what makes the `/clear` → `/recover` sequence above work even for a session
  that has not exited yet. `load_session`, `get_previous_session`, `compact_session`
  and `list_sessions(distill_missing)` all reach the same lazy path.

Distilling by hand — `ctxloom session distill <harp>` — is how you give a session an
essence ahead of needing one, or force a fresh essence over a stale one.

## Overview

Session memory lets you:

1. Clear the context window when you hit limits
2. Recover context wiped from the current session by `/clear`
3. Browse session history to find and load specific sessions

For durable, cross-session work items (distinct from the agent's ephemeral
to-dos), see [Sessions and Tasks](/concepts/sessions-and-tasks/).

## Usage

Session memory is always enabled - no configuration required.

### The `/recover` Command

When you hit context limits and need to clear:

```
/clear
/recover
```

The `/recover` command:

1. Reads the *current* session for this project from disk — the one `/clear` just wiped, which is still live and still growing (read-time — no process tracking)
2. Distills the raw JSONL transcript using a separate LLM (default: Haiku)
3. Returns the essence so you can continue working

Behind the command, `recover_session` does the work. It resolves the active
session by identity — the harp bound to this session at start — and falls
back to the most-recently-touched transcript only if that binding is missing
or its transcript is gone. `get_previous_session` is a different tool, for a
different job: it looks up the session *before* this one, for inspecting
older work, not for undoing a `/clear`.

### Alternative Recovery

You can also recover naturally:

```
What were we working on before the clear?
```

The AI will use `recover_session` to find and distill the current session.
Don't reach for `get_previous_session` here — after `/clear`, "previous
session" language is misleading: the session `/clear` wiped is still the
current one, and `get_previous_session` would return the session before it
instead.

### Browsing Session History

To see recent sessions with short summaries, read the `ctxloom://sessions/recent`
resource — or just ask:

```
Show me recent sessions
```

Then load a specific one to continue it:

```
Load the distilled session from this morning
```

## How It Works

### Session Tracking

ctxloom records each session on disk under the project, in a harp-named session
directory. Recovery is **read-time**: when you ask to recover, ctxloom reads the
current session — the one bound to this session's harp — straight from disk.
That binding is identity, not a timestamp guess; a most-recently-touched
transcript is used only when the harp has no binding or its transcript is
missing. No live process or PID tracking is involved, so recovery works even
after the AI process has fully restarted across `/clear` — and without
manually specifying session IDs.

### Distillation

Distillation happens **outside your session**, in a separate process, using a
separate LLM call. The distilling model reads the full raw transcript from disk,
not whatever fits in your live context window, so it never has to work from a
degraded view of the conversation.

The process:

1. Read transcript: ctxloom reads the raw JSONL session log from disk
2. Chunk: large sessions are split (default: 8000 tokens per chunk)
3. Distill: a fast model (default: Haiku) extracts key information — decisions made and why, context established, progress achieved, next steps planned
4. Store: the result is saved as that session's essence

Distillation compresses aggressively while keeping what a later session needs
to pick up the work.

### Storage

The canonical distilled essence for a session lives under your home directory,
keyed by its harp name:

```
~/.ctxloom/sessions/
├── index.yaml               # Session index (harp name → session metadata)
└── <harp-name>/
    └── essence.md           # Distilled essence for that session
```

`ctxloom session show <harp>` prints this file. Every distill also mirrors
the identical bytes to a project-rooted `.ctxloom/sessions/<session-id>.md`
layout, keyed by backend session ID rather than harp name — that mirror is
the read path `load_session` and `get_previous_session` use when looking a
session up by ID instead of harp name, not a legacy fallback.

### Cross-Agent Workflows

Distilled memory is portable across agents — it's stored as plain markdown. Raw session history is now captured in the same engine-agnostic format regardless of which backend produced it (see "Why ctxloom captures its own transcript" above) — for any session driven through ctxloom's structured chat or a oneshot run. Antigravity is the one backend without a structured chat mode at all, so an interactive Antigravity session falls into the interactive-pty gap noted above and has no raw history captured; running it with a oneshot prompt (`-p`) does.

```bash
# Morning: Write code with Claude
ctxloom run --llm claude-code "implement the auth module"
# When done, just exit. Distill it when you want it: ctxloom session distill <harp>

# Afternoon: Review with Antigravity
ctxloom run --llm antigravity
"Load the distilled session from this morning"
# Antigravity loads the markdown summary, continues the work
```

Use cases:
- Development then review: write with one model, review with another
- Fast then thorough: draft with Haiku, refine with Opus
- Specialist models: use different models for different task types

The distilled markdown captures decisions, progress, and next steps - everything the next agent needs to continue the work.

:::note
Browsing another backend's raw session history (not just distilled summaries) from within a different backend's session isn't a dedicated, documented flow yet, even though the underlying capture format is now shared across backends. If you need it, [open an issue](https://github.com/ctxloom/ctxloom/issues).
:::

## MCP Tools

Session memory provides these MCP tools:

| Tool | Description |
|------|-------------|
| `compact_session` | Force-distil a session's transcript on disk; frees no context in the live conversation and rarely needs to be called directly since distillation already runs on exit, resume, and recovery |
| `load_session` | Distill and load a session by backend session ID or harp name (harp wins) |
| `recover_session` | Recover the current session's context after `/clear` (identity-first: the active harp's bound session, falling back to the most-recently-touched transcript only if that binding is missing) |
| `get_previous_session` | Get the session *before* this one for this project, for inspecting earlier work — not the post-`/clear` path, since `/clear` doesn't change which session is current |

Browsing recent sessions is a **resource**, not a tool — read
`ctxloom://sessions/recent`.

### Example: Forcing a Distill Before Ending

Most sessions never need this — exit, resume, and recovery already trigger it.
Use it when you want the essence ready before you close a session:

```
Distill this session now, I'm about to end it
```

### Example: Load Specific Session

```
Load session swift-amber-falcon
```

`load_session` accepts either a backend session ID (UUID) or a harp name; harp
names are listed in the `ctxloom://sessions/recent` resource.

### Example: Inspect an Earlier Session

```
Use get_previous_session to see what we worked on before this session
```

This is for looking back at a session that already ended — not for recovering
from a `/clear`, which `recover_session` (or just `/recover`) already handles
automatically.

### Example: Browse History

Recent sessions are exposed as the `ctxloom://sessions/recent` resource:

```
Show me recent sessions
```

## Advanced Configuration

The compaction/distillation model is the `fast` role in `llm.defaults`; the
chunk size lives under `config`:

```yaml
llm:
  defaults:
    fast: claude-fast      # config label used for compaction/distillation
config:
  compaction_chunks: 8000  # target tokens per compaction chunk
```

## CLI Commands

### Manual Compaction

```bash
ctxloom memory compact
```

There's no notion of a "current session" from a bare CLI invocation — with no
`--session`, this compacts the most-recently-touched transcript. Pass
`--session <id>` to target a specific one.

### List Sessions

```bash
ctxloom memory list
```

Shows all sessions with their compaction status.

### Managing the Session Index

Sessions are harp-named (e.g. `swift-amber-falcon`) and recorded in the index
at `~/.ctxloom/sessions/index.yaml` automatically once launched with
`ctxloom run`. The `ctxloom session` family reads and manages that index:

```bash
ctxloom session list                    # Sessions for the current project
ctxloom session list --all              # Sessions for every project
ctxloom session show <harp>             # Print a session's distilled essence
ctxloom session rename <old> <new>      # Rename an index entry
ctxloom session delete <harp>           # Drop an entry (files stay on disk)
ctxloom session distill <harp>          # Force-distill a session
```

`session distill` is how a session gets an essence at all — distill first, then load
with `load_session` or `ctxloom run --session <harp>`.

## Best Practices

1. Just `/clear` when needed. Don't overthink it — ctxloom tracks your session automatically.
2. Use `/recover` after clearing. Distillation happens on-demand; there's nothing to pre-save.
3. Browse older sessions by reading `ctxloom://sessions/recent` (or asking "show me recent sessions") when you need context from days ago.
4. Review recovered content and check that the important details were captured.

## Troubleshooting

### Recovery Shows "No Sessions Found"

If recovery can't find a session to distill:
- Ensure you started the session with `ctxloom run` (not raw `claude`), so the session was recorded
- Browse `ctxloom://sessions/recent` (or ask "show me recent sessions") to find and load a specific one

### Compaction Fails

If compaction fails:
- Check that the LLM is configured correctly
- Ensure you have API access for the compaction model
- Try a smaller `config.compaction_chunks` if sessions are very large

### Distilled Session Missing

If a session you expect to load has no distilled content:
- Check `~/.ctxloom/sessions/<harp>/essence.md` for that session's distilled essence
- Run `ctxloom session list` to confirm the session is in the index
- Distill it with `ctxloom session distill <harp>` — sessions have no essence until asked
