# llm-tool-killer (`ltk`)

A small static binary that an LLM coding agent calls as a **pre-tool hook**. It
parses the shell command the agent is about to run and, if a rule matches,
**turns it away and tells the model what to do instead**.

```
go test ./...   ⟶   ✗ "Run tests through the task runner."   →   the model retries with `just test`
```

---

## Why

A plain "deny these commands" hook hits two walls:

1. **It's error-prone** — substring/regex matching misfires on quoting, paths,
   and arguments.
2. **The agent walks around it** — wrapping a blocked command in a sub-shell, a
   variable, or `eval` slips right past naive matching.

`ltk` goes a level deeper: it **parses and understands** the command — resolving
known variables and re-parsing trivial wrappers — then matches rules against the
*real* command, however it's dressed up.

## Scope — read this first

`ltk` is **not a hard compliance control, and not a sandbox.** It's a cooperative
helper that redirects the agent away from operations you'd **rather it not do** —
not operations it must *never, under any circumstances* be able to do.

If you tell the agent to work around a rule, **it will**: write and compile
equivalent code, download the blocked tool and recompile it under a different
name, create a symlink, or any of a hundred other paths. `ltk` understands
*trivial* workarounds and matches the real command behind them; it makes no
claim beyond that. Deeper intent-based detection is aspirational and may never be
feasible.

One bypass deserves special mention precisely because the redirect model leans
on it: the agent can **add a `just`/`make` target that runs the blocked command,
then invoke that target**. The hook only sees the command the agent runs
directly (`just sneaky-test`); the `go test` the task runner spawns is a child
process the hook never inspects — the same property that lets a legitimate
`just test` through. So treat your `justfile`/`Makefile` as part of the trusted
surface, not something the rules can police.

**If you have "never-ever" requirements, run the agent in a sandbox / container.**
That's the right tool for hard isolation; `ltk` is not.

## How it works

```
hook payload (stdin) → engine adapter → resolve shell → parse + understand → match rules → decision (stdout/exit)
```

- The agent picks a **tool** (`Bash`, `PowerShell`), which determines the shell.
- POSIX shells (sh/bash/zsh/mksh) are parsed in-process via
  [`mvdan.cc/sh`](https://github.com/mvdan/sh); **PowerShell** defers to its own
  native parser; **cmd.exe** uses a small built-in lexer. All lower to one common
  command graph.
- **Understanding:** shell variables are resolved against the environment the
  agent runs in plus assignments made in the same command, and trivial wrappers
  (`bash -c`, `eval`, `cmd /c`, `pwsh -Command`) are re-parsed — so a denied
  command can't be smuggled through. Things we genuinely can't resolve (runtime
  values, command output) are left alone, not blocked.
- Rules match the parsed graph, so a denied command is caught inside pipelines,
  `&&`/`;` sequences, subshells, and substitutions too. Quoted text
  (`echo "go test"`) is not a command and does not match.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full design.

## Behavior (living specification)

The scenarios below are the real acceptance tests: `go test ./tests/acceptance/`
**extracts this Gherkin from this README** and runs it against the decision
engine via [godog](https://github.com/cucumber/godog). *This* documented
behavior — the scenarios in the block below — *is* the tested behavior; delete
the block and the suite fails. (Prose and the other snippets in this README are
not executed.)

```gherkin
Feature: Keeping an LLM agent on the project's golden path

  When an agent proposes a command we'd rather it not run, we turn it away and
  point it at the project's preferred way of doing the job. This is a nudge, not
  a lock: it keeps a cooperative agent on the rails, but anyone determined to go
  around it still can.

  Background:
    Given the project asks agents to use:
      | instead of running | use this                    | because                                        |
      | go test            | just test                   | tests run through the task runner              |
      | git tag            |                             | releases go through the pipeline (Versionator) |
      | git push --force   | git push --force-with-lease | a plain force-push can clobber a teammate's work |

  Rule: A discouraged command is turned away, with a reason

    Scenario: Running the tests the direct way
      When the agent runs "go test ./..."
      Then the command is turned away
      And the agent is told "tests run through the task runner"
      And the agent is pointed at "just test"

    Scenario: Tagging a release by hand
      When the agent runs "git tag v1.4.0"
      Then the command is turned away
      And the agent is told "releases go through the pipeline (Versionator)"

    Scenario: An ordinary command is left alone
      When the agent runs "ls -la && cat README.md"
      Then the command is allowed

  Rule: A rule can combine a subcommand and a flag, matched in any order

    # The "git push --force" rule pairs a positional subcommand (push) with an
    # option (--force). The subcommand must come first; the option can sit
    # anywhere, and extra arguments are fine.
    Scenario Outline: A force push is redirected wherever the flag sits
      When the agent runs "<command>"
      Then the command is turned away
      And the agent is pointed at "git push --force-with-lease"

      Examples:
        | command                      |
        | git push --force origin main |
        | git push origin main --force |

    Scenario: The safer form it points to is left alone
      When the agent runs "git push --force-with-lease origin main"
      Then the command is allowed

  Rule: Phrasing the same command differently does not get it through

    Scenario Outline: The discouraged command, dressed up another way
      When the agent runs "<command>"
      Then the command is turned away

      Examples:
        | command             |
        | bash -c 'go test'   |
        | task=test; go $task |

  Rule: We resolve what we can; what only the runtime knows, we leave alone

    # If a value can be worked out before the command runs — a variable assigned
    # in the same command, or one already in the environment — we resolve it and
    # match the real command. So hiding a discouraged command behind a variable
    # and an eval does not get it through.
    Scenario: A discouraged command we can resolve behind an eval is still turned away
      When the agent runs "CMD='go test'; eval $CMD"
      Then the command is turned away
      And the agent is told "tests run through the task runner"

    # If a value is only settled when the command actually runs — an undefined
    # variable, command output, a positional like $1 — we do not guess. We
    # deliberately leave it alone rather than block on a "scary" construct: there
    # is nothing for us to match, so it is a no-op for ltk. This is intentional,
    # not a gap. For hard guarantees, run the agent in a sandbox.
    Scenario: A command whose meaning is only settled when it runs is left alone
      When the agent runs "eval $RESOLVED_AT_RUNTIME"
      Then the command is allowed

  Rule: A turned-away command can be confirmed by running it again

    # ltk points the way; it does not stand guard. If you really mean a command
    # it discouraged, running the exact same command again within a short window
    # lets it through — an explicit, time-boxed escape hatch, not a security
    # control. The window is set by defaults.repeat_window_seconds, and a rule can
    # tune or disable it in its own confirm: block.
    Scenario: Confirming a discouraged command by repeating it
      When the agent runs "go test ./..." and is turned away pending confirmation
      And the agent runs "go test ./..." a second time
      Then the command is allowed

  Rule: A rule can be made inviolate, so repeating never gets it through

    # Some commands shouldn't have an "I really mean it" — a rule marked
    # confirm: { repeat: false } is inviolate: repeating it changes nothing. (A
    # truly determined agent has other paths; for hard guarantees use a sandbox.)
    Scenario: An inviolate command stays turned away no matter how often it is repeated
      Given "git reset --hard" is inviolate
      When the agent runs "git reset --hard" twice and is turned away both times
      Then the command is turned away
```

Run them with `just acceptance`.

## Install

> **Claude Code and Antigravity CLI (`agy`) are supported.** Codex is planned,
> not built — vote 👍 to prioritize:
> [Codex #2](https://github.com/ctxloom/llm-tool-killer/issues/2).
> For Antigravity use `ltk manage install --engine antigravity` (project scope
> only — agy reads hooks solely from the workspace `.agents/hooks.json`).

Build a static binary (no runtime dependencies):

```sh
just build-static      # → bin/ltk
# or: go install github.com/ctxloom/llm-tool-killer/cmd/ltk@latest
```

Register the hook in your agent's config and scaffold a starter rules file.
`manage install` **auto-detects the most relevant agent** (e.g. a `.claude/`
directory) and installs there:

```sh
ltk manage install               # detect engine; write its config + .ltk/config.yaml
ltk manage install --global      # user-level config instead of project
ltk manage install --print       # dry run: show the merged config, write nothing
ltk manage install --force       # overwrite existing rules (old file kept as .bak)
ltk manage uninstall             # cleanly remove the hook again (exact inverse)
```

`install` merges the hook **non-destructively** (your other settings are
preserved). An existing `.ltk/config.yaml` is **never overwritten** — install
warns and keeps your edited rules; pass `--force` to replace them (the old file
is backed up to `.ltk/config.yaml.bak` first). On a fresh project it writes a
starter `.ltk/config.yaml` you can edit and commit. The
starter is ltk's shipped default rule set (documented in
[docs/DEFAULTS.md](docs/DEFAULTS.md)); pass `--no-default-rules` for an empty one.

Commit that `.ltk/config.yaml` alongside your code, and the payoff is automatic: the
next time an agent reaches for a command you've ruled out, it doesn't get a
silent failure or an opaque block — it gets your reason and your suggested
command, and retries the right way.

<details>
<summary>Manual Claude Code settings.json</summary>

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash|PowerShell|Edit|Write|MultiEdit|NotebookEdit",
        "hooks": [ { "type": "command", "command": "ltk evaluate --config .ltk/config.yaml" } ] }
    ]
  }
}
```
</details>

## Rules

Rules live in a YAML file (`.ltk/config.yaml` by convention). The first matching `deny`
wins; its `message`/`suggest` is returned to the model. Each rule has a `mode`
(default `enable`): `enable` is a firm denial, `disable` keeps the rule but turns
it off, and `confirm` lets the agent proceed by re-running the exact command
within `defaults.repeat_window_seconds` (or a per-rule `window_seconds`) — an
explicit, time-boxed escape hatch, not a security control. A `confirm` rule can
add `delay_seconds` to *ignore* an immediate repeat until N seconds pass — this
removes the "repeating is quicker than complying" incentive and blocks the
reflexive instant retry (see [Rule mode](docs/RULES.md#rule-mode)).

The set `ltk` ships with — and writes on `manage install` — is documented rule by
rule, with the rationale for each, in [docs/DEFAULTS.md](docs/DEFAULTS.md).

```yaml
version: 1

defaults:
  on_parse_error: allow   # if a command can't be parsed at all: allow (fail-open) | deny

rules:
  - id: tests-via-task-runner
    match: { command: [go, test] }      # also matches `go --mod=mod test …`
    message: "Run tests through the task runner."
    suggest: "just test"

  - id: no-git-tag
    # `unless` exempts the read-only listing form: `git tag --list` is fine.
    match: { command: [git, tag], unless: ["--list", "-l"] }
    message: "Releases go through the pipeline (Versionator)."

  # Mixed: positional subcommand `push` + option `--force`. The subcommand must
  # come first; the flag matches in any position, and extra args are fine — so
  # this catches `git push --force origin main` and `git push origin main --force`.
  - id: no-force-push
    match: { command: [git, push, --force] }
    message: "Plain --force can clobber a teammate's work."
    suggest: "git push --force-with-lease"

  - id: no-shell-wrapper
    match: { command: "sh -c" }         # program + option, no positional
```

`command` is an argv pattern: a **program** (matched by name or basename),
**positional** args (subcommands, order matters), and **options** (flags, order
doesn't). The full model — including cross-shell portability — is in
[docs/RULES.md](docs/RULES.md).

### Guarding file edits, not just commands

A rule can instead match the agent's **file-editing** tools (Edit, Write,
MultiEdit, NotebookEdit) with `match.path` — for files a tool owns, a whole
directory, or every git submodule. Patterns are full globs (`*`, `**`, `{a,b}`);
a trailing slash means the whole subtree.

```yaml
rules:
  - id: no-hand-edit-version
    match: { path: [VERSION] }            # a tool-owned file (any glob)
    message: "VERSION is managed by the release tool — don't hand-edit it."

  - id: no-edit-vendored
    match: { path: [vendor/] }            # a whole directory subtree

  - id: no-edit-submodules
    match: { path: ["@submodules"] }      # every path in .gitmodules, expanded
    message: "This file lives in a git submodule — edit it in that repo, not here."
```

`manage install` registers the hook for the editing tools as well as the shell
tools (matcher `Bash|PowerShell|Edit|Write|MultiEdit|NotebookEdit`), so path
rules fire out of the box. Details in [docs/RULES.md](docs/RULES.md#matching-file-edits-matchpath).

### This repo runs its own rules

`ltk` dogfoods itself. The rules enforced against agents working in *this*
repository live in [`.ltk/config.yaml`](.ltk/config.yaml), wired in through the `PreToolUse`
hook in [`.claude/settings.json`](.claude/settings.json). They redirect
`go test` / `go build` / `go vet` to the `just` recipes, send releases through
[Versionator](https://github.com/benjaminabbitt/versionator) instead of a
hand-cut `git tag`, swap `git push --force` for `--force-with-lease`, and point
`golangci-lint` at the gocyclo gate (the pinned golangci-lint can't analyze this
go1.25 module). It also carries cooperative nudges off common LLM-agent
footguns — `--no-verify`, `git reset --hard` / `git clean`, blanket `git add -A`,
rewriting the git identity, `rm -rf`, `sudo`, `pkill`/`killall`, and whole-tree
`gofmt -w`. See [`.ltk/config.yaml`](.ltk/config.yaml) for the live set. The hook runs
`bin/ltk`, so build it first with `just build`.

It works. Here's the hook catching this project's own coding agent reaching for
`go test` while building ltk:

```
● Bash(go test ./tools/extract-defaults/ ./cmd/ltk/ 2>&1 | tail -20)
  ⎿  Error: Run tests through the task runner so the whole suite (incl. the README-driven acceptance tests)
     runs the same way CI does.

     If you really mean it, run the exact same command again within 30s to proceed.

     Use instead: just test
```

## Supported

| | |
|---|---|
| **Shells** | sh, bash, zsh, mksh (in-process via mvdan/sh); PowerShell (native parser); cmd.exe (built-in lexer). Variable resolution: shell family. |
| **Engines** | **Claude Code and Antigravity CLI** (`PreToolUse`, gating both shell commands and file edits). Codex is planned, not built — vote 👍 to prioritize: [Codex #2](https://github.com/ctxloom/llm-tool-killer/issues/2). |

## Develop

```sh
just                # list recipes
just check          # fmt-check + vet + complexity + test
just test           # go test ./... (includes the README-driven acceptance suite)
just acceptance     # run the embedded Gherkin with pretty output
just complexity     # cyclomatic-complexity gate (gocyclo, fails > 15)
just build-static   # static release binary
```

The complexity gate uses [`gocyclo`](https://github.com/fzipp/gocyclo), pinned
via a `go.mod` tool directive. Versions are stamped from
[Versionator](https://github.com/benjaminabbitt/versionator) (`ltk --version`).

## License

BSD 3-Clause. See [LICENSE](LICENSE).
