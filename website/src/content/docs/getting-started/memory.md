---
title: "Session Memory"
---

You hit a context limit, or you want a clean slate mid-task. Normally that means `/clear` and then rebuilding your bearings by hand: scrolling back through the old conversation, pasting chunks of it into the fresh one, guessing which decisions and half-finished threads still matter. You grab too much or miss the one detail that mattered.

With ctxloom, you clear and then ask:

```
/clear
What were we working on before the clear?
```

`/clear` empties the window but doesn't end the session; the same session keeps growing underneath it. The agent answers by calling ctxloom's `recover_session` MCP tool, which reads that session's transcript from disk and hands back a fresh distillation of it.

This works in any session started with `ctxloom run`, because every such session carries ctxloom's MCP server. A Claude Code you launch yourself has no ctxloom MCP server and no session record, so there is nothing to recover.

## Where the transcript comes from

ctxloom keeps one canonical, engine-agnostic transcript per session, at `~/.ctxloom/sessions/<harp>/persist/transcript.jsonl`. See [docs/transcript-schema.md](https://github.com/ctxloom/ctxloom/blob/main/docs/transcript-schema.md) for the schema. How it gets filled depends on how the engine was driven:

- An interactive `ctxloom run` drives Claude Code's own terminal UI. ctxloom imports Claude Code's JSONL transcript into the canonical one when the session exits, and `recover_session` runs the same import mid-session. The reader is version-scoped: `ctxloom doctor` reports which reader your installed Claude Code version selects, and a transcript in a format no reader covers is refused rather than guessed at.
- A structured session, such as a child launched with `agent_run`, streams through ctxloom's own process, and every message, tool call and tool result is recorded as it happens.
- `ctxloom run --one-shot` records only the prompt you sent and the reply that came back.

`ctxloom session transcript` lists, watches and purges these transcripts.

## Why this runs out of band

Your harness's own compaction (`/compact` or its auto-compact equivalent) is the right tool for live context pressure, and ctxloom doesn't compete with it.

Session memory solves a different problem: a summary that outlives the session. In-context compaction asks the agent to summarize itself at the moment it has the least room to think, and whatever it drops on the way out is gone after `/clear`.

ctxloom distills out of band instead. It reads the transcript from disk, in a separate process, using the `fast` LLM from your config (Haiku, as `ctxloom init` scaffolds it). The summary is saved to disk, so it's still there after `/clear`, after the process restarts, or a day later.

Distillation is **on-demand**. Nothing distills a session automatically when it ends; a session stays title-less until something asks for its essence. These ask for one:

- `recover_session`, `load_session`, `get_previous_session`, `compact_session` and `list_sessions` with `distill_missing`, over MCP.
- `ctxloom run --session <harp> --distill`, which distills the named session first if it has no essence yet.
- `ctxloom session list --distill`, which distills every listed session whose essence is missing or stale.
- `ctxloom session distill <harp>`, which gives a session an essence ahead of need, or replaces a stale one.

For durable, cross-session work items (distinct from the agent's ephemeral to-dos), see [Sessions and Tasks](/concepts/sessions-and-tasks/).

## Recovering after `/clear`

Ask in plain language:

```
What were we working on before the clear?
```

`recover_session` resolves the active session by identity (the harp bound to this session at start) and falls back to the most-recently-touched transcript only if that binding is missing or its transcript is gone. Recovery is read-time: no live process or PID tracking is involved, so it works even after the engine process has restarted.

Don't reach for `get_previous_session` here. After `/clear`, "previous session" is misleading: the session `/clear` wiped is still the current one, and `get_previous_session` returns the session before it.

## Picking up an earlier session

Every `ctxloom run` prints a short banner before the engine starts, and when this project has an earlier session it names it:

```
previous session: quiet-loyal-otter — bring it back in-session with the "resume" skill
```

To bring it back, either ask inside the new session:

```
Load session quiet-loyal-otter
```

which calls `load_session` (it accepts a harp name or a backend session ID, and the harp wins), or start a new run from it:

```bash
ctxloom run --session quiet-loyal-otter            # fold its full transcript into this run
ctxloom run --session quiet-loyal-otter --distill  # fold in its distilled essence instead
```

To find a session, ask the agent to list recent sessions (it calls `list_sessions`), or run `ctxloom session list`.

## Distillation

The distilling model reads the transcript from disk, not whatever fits in your live context window. It makes one call over the whole transcript. A transcript larger than the distillation budget is reduced first, compressing the oldest content hardest and keeping the most recent intact, because the tail is what the next session needs to pick up the work. The result is saved as that session's essence.

## Storage

Each session is a directory under your home, keyed by its harp name:

```
~/.ctxloom/sessions/<harp>/
├── session.yaml               # the session record: project, engine, bound session ids
├── essence.md                 # the distilled essence, once something asks for one
└── persist/transcript.jsonl   # the canonical transcript
```

`ctxloom session show <harp>` prints the essence.

## Cross-model workflows

The essence is plain markdown, so a session run on one model can be picked up on another:

```bash
# Morning: write code with the default model
ctxloom run --llm claude-code "implement the auth module"
# When done, exit. Distill it when you want it: ctxloom session distill <harp>

# Afternoon: review with another configured label
ctxloom run --llm claude-fast --session <harp> --distill "review what was done"
```

`--llm` takes a label from `llm.configs` in your config; `claude-code` and `claude-fast` are the ones `ctxloom init` scaffolds.

## MCP tools

| Tool | Description |
|------|-------------|
| `recover_session` | Recover the current session's context after `/clear` (identity-first: the active harp's bound session, falling back to the most-recently-touched transcript only if that binding is missing) |
| `load_session` | Distill and load a session by backend session ID or harp name (harp wins) |
| `get_previous_session` | Get the session *before* this one for this project, for inspecting earlier work. Not the post-`/clear` path, since `/clear` doesn't change which session is current |
| `list_sessions` | List recent sessions; `distill_missing` distills the ones without an essence first |
| `compact_session` | Distill a session's transcript on disk for a later session to pick up. It frees no context in the live conversation |

See the [MCP tools reference](/reference/mcp-tools/) for every parameter.

## Configuration

The distillation model is the `fast` role in `llm.defaults`:

```yaml
llm:
  defaults:
    fast: claude-fast      # config label used for distillation
```

## CLI commands

Sessions are harp-named (e.g. `swift-amber-falcon`) and recorded automatically once launched with `ctxloom run`. The `ctxloom session` family reads and manages them:

```bash
ctxloom session list                      # Sessions for the current project
ctxloom session list --all                # Sessions for every project
ctxloom session show <harp>               # Print a session's distilled essence
ctxloom session distill <harp>            # Distill a session now
ctxloom session edit <harp> --name <new>  # Rename a session
ctxloom session remove <harp> --yes       # Remove record, transcript and essence
ctxloom session purge <harp> --yes        # Empty a session, keep it listed
```

`remove` and `purge` only report what they would do until you pass `--yes`, and both refuse a session that was never distilled, because that would destroy the only record of it.

## Troubleshooting

### Recovery finds no session

- Make sure the session was started with `ctxloom run` (not raw `claude`), so it was recorded and has ctxloom's MCP server.
- Run `ctxloom session list` to confirm the session exists, then load it by harp name.

### Distillation fails

- Check that the `fast` LLM label resolves: `ctxloom llm list`.
- Make sure that engine is installed and authenticated.

### A session has no essence

- Check `~/.ctxloom/sessions/<harp>/essence.md`.
- Distill it with `ctxloom session distill <harp>`. Sessions have no essence until something asks for one.
