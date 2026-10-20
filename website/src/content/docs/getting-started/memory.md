---
title: "Session History"
---

Every `ctxloom run` is a harp-named session (e.g. `swift-amber-falcon`), and ctxloom keeps a record of it: one canonical, engine-agnostic transcript you own. You can find a session again by what you asked it, read its record, and fold its conversation into a new run.

ctxloom does **not** compact sessions. Your engine compacts its own context (`/compact`, or its auto-compact) and resumes its own sessions (Claude Code's `/resume`); ctxloom leaves both to it.

## Where the transcript comes from

The transcript lives at `~/.ctxloom/sessions/<harp>/transcripts/transcript.jsonl`. See [docs/transcript-schema.md](https://github.com/ctxloom/ctxloom/blob/main/docs/transcript-schema.md) for the schema. How it gets filled depends on how the engine was driven:

- An interactive `ctxloom run` drives Claude Code's own terminal UI. ctxloom imports Claude Code's JSONL transcript into the canonical one when the session exits. The reader is version-scoped: `ctxloom doctor` reports which reader your installed Claude Code version selects, and a transcript in a format no reader covers is refused rather than guessed at.
- A structured session, such as a child launched with `agent_run`, streams through ctxloom's own process, and every message, tool call and tool result is recorded as it happens.
- `ctxloom run --one-shot` records only the prompt you sent and the reply that came back.

`ctxloom session transcript` lists, shows, watches and purges these transcripts.

## Reading a transcript

`session transcript show` prints a session's whole transcript and exits, so you can save it or pipe it. It refreshes the transcript from your engine's log first, and writes nothing but the transcript to stdout:

```bash
ctxloom session transcript show <harp> --format text > session.txt
ctxloom session transcript show <harp> --format json | jq -r 'select(.entry.type == "user") | .entry.content'
```

The output is `session transcript watch`'s: text prints each turn with a rule after it, and `--format json` prints one JSON event per line (`{"entry": …}` or `{"boundary": …}`), so a jq filter written for one works on the other. Piped or redirected with no `--format`, the output is JSON (your `non_tty_format`). To follow a session while it runs, use `session transcript watch <harp>`.

## Finding a session

A session's title is the first thing you asked it: the first prompt in its transcript, cut to one line. `session list` and `session search` show it:

```bash
ctxloom session list                      # Sessions for the current project
ctxloom session list --all                # Sessions for every project
ctxloom session search worktree layout    # Sessions whose name, title or times match every word
ctxloom session show <harp>               # One session's record: title, state, engine, times
```

A session whose transcript holds no prompt shows `(no summary)`.

## Picking up an earlier session

Start a new run from it:

```bash
ctxloom run --session quiet-loyal-otter
```

This folds the session's full recorded transcript into the new run's assembled context, so the model sees the conversation you had.

## Keeping and removing transcripts

A transcript is a session's record, so ctxloom keeps it until the session ended more than `session_transcript_retention` ago (default `30d`). Until then every command that destroys transcripts refuses it: `session transcript purge`, `session purge`, `session remove`, `session sweep` and `clean --include-persist`.

To keep one for good:

```bash
ctxloom session keep <harp>          # kept at any age; session list shows it as kept
ctxloom session keep <harp> --undo   # releases it
```

```bash
ctxloom session edit <harp> --name <new>  # Rename a session
ctxloom session remove <harp> --yes       # Remove its record and transcript
ctxloom session purge <harp> --yes        # Empty a session, keep it listed
```

`remove` and `purge` only report what they would do until you pass `--yes`.

## Storage

Each session keeps its machine state in a directory under your home, keyed by its harp name, and its readable outputs in an output dir under your Documents folder (`<Documents>/ctxloom/<project>/<harp>/`, or wherever the `output_dir` config key says):

```
~/.ctxloom/sessions/<harp>/
├── session.yaml                    # the session record: project, engine, bound session ids, output dir, keep
└── transcripts/transcript.jsonl    # the canonical transcript

<Documents>/ctxloom/<project>/<harp>/
└── *.plan.md                       # the session's plans
```

Nothing ctxloom runs deletes the output dir. Summaries an older ctxloom wrote there (`essence.md`, `segments/*.md`, `next-step.md`) stay as notes; nothing reads or removes them.

## Troubleshooting

### A session is missing from the list

- Make sure the session was started with `ctxloom run` (not raw `claude`), so it was recorded.
- `ctxloom session list --all` lists every project's sessions.

### A session has no title

- Its transcript holds no prompt yet, or it was never captured: `ctxloom session transcript list` says which sessions have one.
