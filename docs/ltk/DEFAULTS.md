# Shipped default rules

This document is the **source of truth** for the rule set `ltk` ships with and
writes on `ltk manage install` (unless `--no-default-rules`). The fenced `yaml`
blocks below are extracted, in order, into [`cmd/ltk/sample.ltk.yaml`](../cmd/ltk/sample.ltk.yaml)
(which is embedded in the binary) by [`tools/extract-defaults`](../tools/extract-defaults).
A lefthook pre-commit hook regenerates and fails the commit on drift, so the doc
and the shipped file can never disagree. To regenerate by hand: `just defaults`.

Each rule below is a YAML chunk followed by **why it's a default**. Every default
is a *cooperative nudge* — it turns a command away and points at the safer path;
it is not a security boundary (for hard isolation, run the agent in a container).

Rules use `mode` (default `enable`, a firm denial). Workflow redirects ship as
`mode: confirm` — you can still run the raw command by repeating it after the
delay and within the window — while the destructive-action guards stay firm
(`enable`), so repeating them changes nothing. See [RULES.md](RULES.md#rule-mode).

## Header

The document opens with the config header: fail open on unparseable input, and
the window/delay for any `mode: confirm` rule. A denied command can be re-run to
proceed, but only after a 10s **delay** and within a 30s **window** — so an
immediate, reflexive repeat is ignored, and only a deliberate one (after the
pause) goes through. It's an escape hatch, not a control.

```yaml
version: 1

defaults:
  on_parse_error: allow
  repeat_window_seconds: 30
  repeat_delay_seconds: 10

rules:
```

## Tests through the task runner

The canonical redirect, and the one users most often customize. Agents reach for
`go test` directly; routing through the task runner keeps the suite, env, and
flags identical to CI. Ships as `mode: confirm` — repeat to run raw `go test`
anyway. Edit this for your stack (`make test`, `npm test`, …) or set
`mode: disable`.

The shipped default names no specific runner (no `just`/`make`/`npm` assumption);
fill in your project's command in `suggest` when you adopt it.

```yaml
  - id: tests-via-task-runner
    match: { command: [go, test] }
    mode: confirm
    message: "Run tests through your project's task runner, not the compiler directly, so the suite matches CI."
```

## Don't discard uncommitted work

`git reset --hard` is the single most-reported agent disaster: it silently and
irreversibly throws away uncommitted changes. Stash or commit first.

```yaml
  - id: no-reset-hard
    match: { command: [git, reset, --hard] }
    message: "`git reset --hard` discards uncommitted work irreversibly. Stash or commit first."
    suggest: "git stash --include-untracked"
```

`git clean` deletes untracked files for good — easy for an agent to fire while
"tidying up" and wipe new, unsaved files. The dry-run forms (`-n` / `--dry-run`)
only preview, so they're exempt.

```yaml
  - id: no-git-clean
    match:
      command: [git, clean]
      unless: ["-n", "--dry-run"]
    message: "`git clean` deletes untracked files for good. Preview with `-n`, or stash."
    suggest: "git stash --include-untracked"
```

## Don't bypass the gate

Agents habitually add `--no-verify` to slip past a failing pre-commit/pre-push
hook instead of fixing what it caught — the most common way guardrails get
quietly defeated.

```yaml
  - id: no-skip-commit-hooks
    match: { command: [git, commit, --no-verify] }
    message: "Don't bypass commit hooks with --no-verify — fix what the hook flags, then commit."
```

```yaml
  - id: no-skip-push-hooks
    match: { command: [git, push, --no-verify] }
    message: "Don't bypass push hooks with --no-verify — fix the failure, then push."
```

A plain `--force` push can overwrite a teammate's commits; `--force-with-lease`
refuses if the remote moved.

```yaml
  - id: no-force-push
    match: { command: [git, push, --force] }
    message: "A plain force-push can overwrite a teammate's commits."
    suggest: "git push --force-with-lease"
```

## Keep commits scoped and correctly attributed

`git add -A` / `git add .` sweeps in files the task never touched (and can stage
deletions), so the default is to stage in-scope paths explicitly. But blanket
staging is genuinely the right call when a change spans a large file set — so
this ships as `mode: confirm`: it nudges once, and repeating the command stages
everything.

```yaml
  - id: stage-explicitly
    match: { command: [git, add], args_any: ["-A", "--all", "."] }
    mode: confirm
    message: "Prefer staging the paths this change actually touches. If the change really does span many files, run the same command again to stage them all."
    suggest: "git add <path> [<path> …]"
```

The agent should commit as you, with an identity configured once outside the
session — not rewrite `user.name`/`user.email` per command.

```yaml
  - id: no-rewrite-git-identity
    match: { command: [git, config], args_any: ["user.email", "user.name"] }
    message: "Don't change the git identity from inside the agent; set it once in your own global config."
```

## Destructive shell and privilege

A recursive force-delete with a typo'd path is catastrophic and irreversible.
`-rf`, `-fr`, and `-r -f` are all caught (bundled short options are split for
matching under POSIX shells).

```yaml
  - id: rm-rf-careful
    match: { command: rm, args_all: ["-r", "-f"] }
    message: "Double-check the path before a recursive force-delete; delete specific paths, not trees, when you can."
```

An unattended agent loop has no business escalating privileges.

```yaml
  - id: no-sudo
    match: { command: sudo }
    message: "Don't escalate privileges from inside the agent — ask a human to run anything that needs sudo."
```

Killing processes by name (`pkill`/`killall`) is a blunt instrument — it can hit
the wrong PID, your editor, or a sibling agent. Target a specific PID instead.

```yaml
  - id: no-pkill
    match: { command: pkill }
    message: "Avoid pkill by name (wrong-PID risk) — find the specific PID and `kill` it."
```

```yaml
  - id: no-killall
    match: { command: killall }
    message: "Avoid killall by name (wrong-PID risk) — find the specific PID and `kill` it."
```

## Don't run commands the gate can't read

`pwsh -EncodedCommand` takes a Base64-encoded script, so neither a human
skimming the transcript nor ltk's wrapper expansion can see what it actually
runs — it is the classic way a denied command slips past inspection. There is
no agent workflow that needs it: the same command passed plainly via
`-Command` works and stays inspectable. Both rules ship firm (`enable`).
The `args_any` list covers the documented aliases (`-e`, `-ec`, `-enc`) and the
common casings (argument matching is literal).

```yaml
  - id: no-encoded-command-pwsh
    match:
      command: [pwsh]
      args_any: ["-EncodedCommand", "-encodedcommand", "-e", "-ec", "-enc"]
    message: "Encoded commands can't be inspected. Pass the script in plain text instead."
    suggest: "pwsh -Command '<the same script, unencoded>'"
```

```yaml
  - id: no-encoded-command-powershell
    match:
      command: [powershell]
      args_any: ["-EncodedCommand", "-encodedcommand", "-e", "-ec", "-enc"]
    message: "Encoded commands can't be inspected. Pass the script in plain text instead."
    suggest: "powershell -Command '<the same script, unencoded>'"
```

## Don't disturb pinned git submodules

These are **file-edit** rules (`match.path`), not command rules — they gate the
agent's Edit/Write/MultiEdit/NotebookEdit tools. A submodule's working tree is a
separate repo pinned at a commit; editing its files from the superproject is
almost always a mistake (the change isn't committed where the agent expects and
gets lost). `@submodules` expands to every path in `.gitmodules`, so this one
rule covers all of them without naming them and stays correct as they change.

Unlike the workflow nudges above, these ship `enable` (**firm** — repeating does
not lift them): a submodule's contents belong to that submodule's own repo, so
there is no "override and edit it from here" that's correct. Make the edit in the
submodule and commit it there.

```yaml
  - id: no-edit-submodules
    match: { path: ["@submodules"] }
    message: "This file is inside a git submodule (a pinned, separate repo). Edit it in that submodule's own repo and commit there — never from the superproject. This is firm; there is no override."
```

`.gitmodules` itself defines the submodule set; hand-editing it desyncs the
recorded gitlinks from the config. Add/remove submodules with `git submodule`,
not a text edit.

```yaml
  - id: no-edit-gitmodules
    match: { path: [.gitmodules] }
    message: "Don't hand-edit .gitmodules — use `git submodule add`/`deinit` so the config and the recorded gitlink stay in sync."
```
