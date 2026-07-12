---
title: "Writing rules"
description: "The ltk rule file: how commands are matched, how file rules work, and how a denial reaches the model."
sidebar:
  order: 1
---

Rules live in `.ltk/config.yaml`. `ltk manage install` writes a starter file; from
then on it is yours to edit, and it belongs in git alongside the code it guards.

```yaml
version: 1

defaults:
  shell: bash               # fallback dialect when nothing else determines one
  on_parse_error: allow     # command couldn't be parsed at all: allow | deny
  repeat_window_seconds: 30 # window for `mode: confirm` rules
  repeat_delay_seconds: 10  # minimum wait before a confirm repeat counts

rules:
  - id: go-test-to-just            # required, unique
    match: { command: [go, test] }
    action: deny                   # deny (default) | allow
    message: "Use `just test`."    # shown to the model on deny
    suggest: "just test"           # optional replacement command
    mode: enable                   # enable (default) | confirm | disable
```

Unknown keys are rejected, so a typo like `programm:` is an error rather than a
silent no-op. Two rules with the same `id` are a load error too.

## How a rule fires

Every command an agent proposes is parsed into a command graph, and each command
in that graph is tested against the rules in file order. The first matching
`deny` wins; its `message` and `suggest` go back to the model so it can retry the
right way. An `allow` rule that matches first clears the command and stops the
search, so an earlier `allow` shadows a later `deny` — order is the whole
control surface.

Matching every command in the graph means a denied command is caught however it
is wrapped: inside pipelines, `&&`/`||`/`;` sequences, subshells, brace groups,
command substitution, process substitution, assignments, backgrounding, and
`if`/`for`/`while` bodies. Quoted text is not a command, so `echo "go test"` does
not trip a `go test` rule.

## Matching commands

`match.command` is the program plus its arguments. Write it as a string (split on
whitespace) or a list (taken verbatim):

```yaml
match: { command: "go test" }   # same as
match: { command: [go, test] }
```

This is neither a glob nor a regex, and that distinction is where most broken
rules come from. Tokens are classified by kind, and each kind matches differently:

| Kind | What it is | How it matches |
|---|---|---|
| Program | the first token | `argv[0]` exactly **or by basename**, so `/usr/local/go/bin/go` matches `go` |
| Positional | a non-option token (`test`, `commit`) | order matters — must appear in order, as an ordered *subsequence*, among the command's non-option arguments |
| Option | any flag (`-c`, `--no-cache`) | order does not matter — must appear somewhere in the arguments, and never consumes a positional slot |

Trailing arguments are always allowed. A pattern may mix positionals and options
in any list order, so `[docker, --debug, build]` and `[docker, build, --debug]`
are the same rule.

A subcommand's position carries meaning (`go test` and `go help test` are
different operations), so positionals must appear in order. An option's position
does not, so options match as an unordered set. Options are skipped when locating
positionals, and the match is a subsequence rather than a strict prefix, which
means a value-taking flag whose value lands among the operands cannot push the
subcommand out of position. `go --mod=mod test`, `git -C /repo push`, and
`docker --context prod build` all still match `[go, test]`, `[git, push]`, and
`[docker, build]`. The trade-off is that a positional can match a non-leading
operand of the same spelling, which only ever widens what a deny rule catches.

```yaml
match: { command: go }                                # any `go …`
match: { command: [go, test] }                        # `go test …`, `go --mod=mod test …`
match: { command: "sh -c" }                           # `sh -c …`, `sh -e -c …` (NOT `sh -e`)
match: { command: [git, push, --force, --no-verify] } # those flags in any order after `push`
```

### Refining a match

`args_any` and `args_all` are positive filters on the arguments: the listed
tokens must be present.

```yaml
match:
  command: docker
  args_any: [build, buildx]     # at least one present anywhere in args
  args_all: ["--push", "--tag"] # all present anywhere in args
```

`unless` is the negative one. If the command carries any listed token, the rule
does not match. This is how you carve out the read-only form of a command you
otherwise block.

```yaml
- id: no-git-tag
  match:
    command: [git, tag]
    unless: ["--list", "-l", "-n"]   # `git tag --list` is fine
  message: "Releases go through the pipeline, not a hand-cut `git tag`."
```

Other honest `unless` cases are `rsync`, `make`, and `helm upgrade` with
`--dry-run`. Note that `--dry-run` is not universal: `docker build` and
`docker run` have none, only `docker compose` does, so use the flag the target
command actually supports.

Bundled short options are expanded before the argument conditions are checked, so
`-n` in `unless` also matches `rm -rn`, and `args_all: [-r, -f]` catches `rm -rf`,
`rm -fr`, and `rm -r -f` alike. Only POSIX shells bundle this way; `cmd`
(`/switch`) and PowerShell (`-LongName`) tokens are never split. The command
itself is never rewritten — this is a matcher-level convenience.

### Shells

Flag syntax differs by dialect, so token classification is shell-aware.

| Shell family | A token is an option when it… |
|---|---|
| sh, bash, zsh, mksh | starts with `-`. A lone `-` is positional (stdin). |
| pwsh | starts with `-` (`-Path`, `-Recurse`). |
| cmd | starts with `/` (`/c`, `/s`) or `-`. |

The `cmd` case is why this matters: under cmd, `/q` is a switch, while under a
POSIX shell `/usr/bin/x` is a path. `match.shells` narrows a rule to a list of
dialects, and an absent or empty `shells` means every shell. Valid entries are
`sh`, `bash`, `zsh`, `mksh`, `pwsh`, and `cmd`; anything else is a config error.

```yaml
- id: no-cmd-rmdir
  match:
    command: [rmdir, /s]   # cmd's recursive delete; /s is its switch
    shells: [cmd]
  message: "Don't recursively delete directories from the agent."
```

`shells` is a gate evaluated before the command pattern, so a rule whose shell
does not match is skipped outright. Reach for it when a rule only classifies
correctly on certain dialects, or when it names a shell-specific builtin. Most
rules (`git`, `go`, `rm`) mean the same thing everywhere and should omit it.

The shell in question is the one ltk resolved for that command, not the one you
typed it in. A wrapped inner command is re-parsed under the inner shell, so
`pwsh -Command "..."` invoked from bash yields commands whose shell is `pwsh`.
When a rule mysteriously fails to fire, check the resolved shell first.

## Understanding, not blocking

Before matching, a command is resolved as far as is statically possible, so a
trivial wrapper or a variable can't sneak a denied command past a rule. Variable
dereferences expand against the process environment plus assignments seen earlier
in the same command, so `t=test; go $t` matches a `go test` rule. Values that
can't be known statically, like command output or `$1`, expand to empty and
simply don't match. The inner command of a trivial wrapper (`bash -c "…"`,
`eval "…"`, `cmd /c "…"`, `pwsh -Command "…"`) is re-parsed and matched too.

This is not a security boundary. An agent instructed to work around a rule can
rename the tool, recompile it, or symlink it. For hard limits, run the agent in a
container.

## Rule mode

Every rule has a `mode`, defaulting to `enable`, that decides how firmly a deny
holds.

| mode | behavior |
|---|---|
| `enable` (default) | Inviolate. Nothing the agent does in-band lifts the denial. |
| `confirm` | Soft. The first attempt is denied with a hint; re-running the *exact* same command within the window is then allowed. |
| `disable` | The rule stays in the file but never matches. |

The confirm window comes from `defaults.repeat_window_seconds` or a per-rule
`window_seconds`. A `confirm` rule with no effective window can never be
satisfied, so it is rejected at load rather than left to mislead you.

`confirm` is defeatable by design — the agent that produced the command can
reproduce it, and a repeat is faster than complying, which is exactly why an agent
reaches for it. A delay inverts that incentive. `delay_seconds` (or
`defaults.repeat_delay_seconds`) ignores the repeat until N seconds after the
first denial, then honors it up to the window, so the override lives in the band
`[delay, window]`. The delay must be shorter than the window.

```yaml
- id: tests-via-task-runner
  match: { command: [go, test] }
  mode: confirm
  delay_seconds: 10   # ignore an immediate repeat; honor one after 10s
  window_seconds: 60  # …up to 60s after the first denial
  message: "Run tests through the task runner."
  suggest: "just test"
```

The delay does not make `confirm` a control; a determined agent can sleep and
retry. What it buys is behavioral. It removes the "bypass is quicker than
compliance" incentive and turns a reflexive retry into a deliberate wait. For
something that must not be overridable, use `mode: enable`.

## Matching file edits

Most rules guard shell commands. A rule can instead guard the agent's own editing
tools (Edit, Write, MultiEdit, NotebookEdit) with `match.path`, which is useful
for files owned by a tool and not meant to be hand-edited.

```yaml
- id: no-hand-edit-version
  match: { path: [VERSION] }
  message: "VERSION is managed by the release tool — use `just bump`, not a hand edit."
```

Unlike `match.command`, `path` patterns are real globs (`*`, `?`, `[…]`, `{a,b}`,
and `**`, which spans directory separators). Backslashes are normalized to `/`
first. The editing tools always pass an absolute path, so each pattern is tried
three ways: against the file's basename, so `*.lock` catches `/proj/a/b/c.lock`;
against the full path, so an absolute pattern matches as written; and against the
full path with an implicit `**/` prefix, so `src/**/*.go` catches `/proj/src/a/b.go`
and `dist/*` catches `/proj/dist/x` but not the deeper `dist/x/y`.

A trailing slash is directory sugar. `path: [vendor/]` expands to `vendor/**` and
blocks every file under any `vendor` directory at any depth — this is how you
prohibit writes to a whole subtree.

The reserved pattern `@submodules` expands at evaluate time to a subtree for every
path in the repo's `.gitmodules`, so one rule blocks edits inside all submodules
without naming them and stays correct as they come and go. With no `.gitmodules`
it matches nothing.

```yaml
- id: no-edit-submodules
  match: { path: ["@submodules"] }
  message: "This file is inside a git submodule — edit it there, not from the superproject."
```

A rule is either a command rule or a path rule, never both: combining `path` with
`command`, `args_*`, or `shells` is a config error. Everything else carries over,
and a `confirm` path rule is satisfied by re-attempting the same edit inside the
window.

For file rules to fire at all, the hook has to be registered for the editing
tools. `ltk manage install` registers the matcher
`Bash|PowerShell|Edit|Write|MultiEdit|NotebookEdit`; a hook narrowed to `Bash`
alone never sees a file edit.

## Testing a rule

Write the rule, then ask ltk what it would do:

```sh
ltk check --command 'go test ./...' --format json
```

```json
{
  "decision": "deny",
  "message": "Use `just test`.",
  "suggestion": "just test"
}
```

`check` reads the same rules the hook does and reports `{decision, message,
suggestion}` as discrete fields, with no confirm-by-repeat state involved. It is
an explicit command, so unlike the hook it fails loud: a broken config exits
non-zero and tells you why, which is what you want while authoring. Run it on a
command you expect to deny *and* one you expect to allow — a rule that never
fires and a rule that catches everything look identical until you check the
second case.
