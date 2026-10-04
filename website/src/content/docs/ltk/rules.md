---
title: "Writing rules"
description: "The ltk rule file: how commands are matched, how file rules work, and how a denial reaches the model."
sidebar:
  order: 1
---

Rules live in `.ltk/config.yaml`. `ltk manage install` writes a starter file; from
then on it is yours to edit, and it belongs in git alongside the code it guards.
The starter file catches generic footguns — the rules worth adding are specific to
your project: the deploy script an agent shouldn't run directly, the migration
that needs a human, the directory nobody should hand-edit. And as on
[the ltk overview](/ltk/): a rule is a guardrail against reflexive mistakes, not a
security boundary, so it stops the accidental path, not a determined one.

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
    suggest: "just test"           # replacement command; also shown on deny
    mode: enable                   # enable (default) | confirm | disable
```

Unknown keys are rejected, so a typo like `programm:` is an error rather than a
silent no-op. Two rules with the same `id` are a load error too.

A deny rule must carry a `message` or a `suggest` — either alone is enough
(`suggest` renders as "Use instead: …"). Without one the model is told `deny`
with no reason and no alternative, so it simply retries; a rule that cannot say
why is a load error. `allow` and `mode: disable` rules are exempt.

## Fail-open, with one exception

Every escape hatch on this page (`unless`, `mode: confirm`,
`defaults.on_parse_error: allow`) is fail-open by design: when ltk cannot fully
resolve a command, an exception token is present, or a rule is deliberately soft,
the command goes through rather than being blocked on uncertainty. Every rule you
write inherits that posture: it redirects a cooperative agent, it does not stop a
determined one.

The one exception is a tool that fired the installed hook but that ltk does not
recognize by name. The installed hook matcher and ltk's runtime tool recognition
come from one list, but a vendor-renamed tool can still fire the matcher while
the exact-name recognition misses it. ltk then cannot read the payload's fields,
so no rule can be evaluated against it. Rather than pass a tool you told ltk to
watch unexamined, ltk denies the call with a reason naming the tool; the fix is
to add it to the engine's gated tools and reinstall (`ltk manage install`).
Tools the matcher does not watch (Read, Grep, …) are never affected.

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

`match.command` is a list: the program, then the operands to look for. Every
element of `command`, `args_any`, `args_all` and `unless` is an
[RE2](https://github.com/google/re2/wiki/Syntax) regular expression matched
against **one whole argument**: it is implicitly anchored, so `push` matches the
argument `push` and not `pushx`, and `push|fetch` matches either and nothing
longer. Matching is case-sensitive unless a pattern opts in with `(?i)`.

```yaml
match: { command: [go, test] }                       # `go test …`
match: { command: [git, 'push|fetch'] }              # either subcommand
match: { command: [git, config, '(?i)user\.name'] }  # git config keys ignore case
```

Single-quote every pattern. In double quotes `"\."` is an invalid YAML escape,
and `|`, `[`, `*`, `?`, `{` and `:` need quoting in a flow list anyway. Escape
regex metacharacters you mean literally: `.` is any one character, so `'\.'`
is the current directory and `'.'` matches every one-character argument.

The two pattern languages are deliberate and per field:

| Field | Language | Matched against |
|---|---|---|
| `command`, `args_any`, `args_all`, `unless` | anchored RE2 regex | one argv element at a time |
| `path` (file rules) | doublestar glob | the edited file's path |

A pattern never sees the command line, only one argument the shell-aware parser
has already split out and classified. That is what keeps a regex here from being
the footgun `sudoers` has carried for decades: `/usr/bin/vim *` reads as "vim
only", but its `*` matches any words the shell hands it, so `vim -- /bin/sh`
matches too. Here no pattern can span two arguments or turn an operand into an
option. RE2 also runs in linear time, so an argument an agent wrote cannot make
matching backtrack. RE2 has no lookahead or backreferences; express a negation
with `unless` or an `allow` rule above the deny.

Arguments are classified by kind before any pattern runs:

| Kind | What it is | How it matches |
|---|---|---|
| Program | `argv[0]` | the first `command` pattern, against `argv[0]` as written **or its basename**, so `/usr/local/go/bin/go` matches `go`. Under `cmd` and `pwsh` both are lowercased and lose a trailing `.exe` first (how Windows resolves a program), so write `cmd`, not `cmd.exe` |
| Operand | a non-option argument (`test`, `commit`, a path) | the remaining `command` patterns, aligned as below |
| Option | any flag (`-c`, `--no-cache`, `/s` under cmd) | only `args_any`, `args_all` and `unless` — never `command` |

Options never go in `command`. A `command` pattern after the program that
begins with a literal `-` is a load error, so `[git, push, --force]` fails
loudly instead of silently never firing; write `command: [git, push],
args_all: ['--force']`. The check reads the pattern's literal prefix, so it
cannot see a dash inside a group like `(-a|-b)`.

### Alignment: allow and deny match operands differently

`match.align` says how the operand patterns line up against the command's
operands. Each action has a default, and the difference is the load-bearing
part of the model:

| `align` | Means | Allowed on |
|---|---|---|
| `subsequence` | the patterns match operands in order, not necessarily leading or adjacent | deny (its default and only alignment) |
| `prefix` | pattern *i* matches operand *i*, from operand 0; anything may follow | allow (its default) |
| `exact` | as `prefix`, and nothing follows: no further operand and no option at all | allow |

For a **deny**, subsequence means a value-taking flag whose value lands among
the operands cannot push the subcommand out of position: `go --mod=mod test`,
`git -C /repo push`, and `docker --context prod build` all still match `[go,
test]`, `[git, push]`, and `[docker, build]`. The trade-off is that a pattern
can match a non-leading operand of the same spelling (a deny `[go, test]` also
catches `go help test`), which only ever widens what a deny catches —
fail-safe for a guard.

For an **allow** that looseness is fail-*open*: it widens what gets let
through. `allow: [git, status]` would otherwise match `git commit -m status
--no-verify` (`status` is `-m`'s value, not the subcommand), silently clearing
a command a deny was there to catch. So allows anchor at operand 0. One
consequence: `docker --context prod build` does **not** match an allow
`[docker, build]`, and there is no position-blind way around that —
`args_any`/`args_all` on an allow rule are a load error, because `args_all:
[build]` would also clear `docker run --name build …`. Such a command falls
through to the deny, which is the safe direction.

`exact` is for clearing one precise invocation whose longer forms must still
reach a deny. It forbids options too, because options are what turn many reads
into writes (`git config --unset user.name` has the read's operands):

```yaml
- id: allow-identity-read
  match: { command: [git, config, 'user\.name'], align: exact }
  action: allow                  # `git config user.name` only
```

Arity runs the other way with a trailing `'.*'` operand, which requires an
operand to exist whatever its text, the empty string included. It is
positional, so it tells a read from a write precisely:

```yaml
- id: no-git-identity-write
  match:
    command: [git, config, '(?i)user\.(name|email)', '.*']
  message: "Set the git identity outside the agent."
```

`git config user.name` (a read) has nothing after the key and passes; `git
config user.name Bob` and `git config user.name ""` are writes and are
denied. A value-option ahead of the subcommand (`git -c k=v config user.name`)
does not count, because it is not after the key.

### Refining a match

`args_any` and `args_all` are positive filters on the arguments, deny rules
only: some argument must match some `args_any` pattern, and every `args_all`
pattern must match some argument.

```yaml
match:
  command: [docker]
  args_any: ['build|buildx']        # at least one present anywhere in args
  args_all: ['--push', '--tag(=.*)?'] # all present anywhere in args
```

`--opt=value` is one argument, so `'--tag(=.*)?'` covers both `--tag x` and
`--tag=x`.

`unless` is the negative one. If any argument matches any listed pattern, the
rule does not match. This is how you carve out the read-only form of a command
you otherwise block, and it can name a shape as well as a flag:

```yaml
- id: no-git-tag
  match:
    command: [git, tag]
    unless: ['--list|-l|-n']   # `git tag --list` is fine
  message: "Releases go through the pipeline, not a hand-cut `git tag`."
- id: install-via-just
  match:
    command: [go, install]
    unless: ['.+@.+']          # `go install mod@ver` installs a third-party tool
  message: "Install through the task runner."
```

Other honest `unless` cases are `rsync`, `make`, and `helm upgrade` with
`--dry-run`. Note that `--dry-run` is not universal: `docker build` and
`docker run` have none, only `docker compose` does, so use the flag the target
command actually supports.

Bundled short options are expanded before the argument conditions are checked, so
`-n` in `unless` also matches `rm -rn`, and `args_all: ['-r', '-f']` catches `rm -rf`,
`rm -fr`, and `rm -r -f` alike. Only POSIX shells bundle this way; `cmd`
(`/switch`) and PowerShell (`-LongName`) tokens are never split. The command
itself is never rewritten — this is a matcher-level convenience, and it lives in
ltk rather than the shell parser because bundling is a per-program getopt
convention (Go's `flag`, `find`, and `dd` don't follow it).

Short aliases are a different thing and are not expanded. ltk does not know that
`-f` is short for `--force` or `-n` for `--dry-run`; that mapping is per-program.
A rule written with only the long form misses the short one: `args_all:
['--force']` does not catch `git push -f`. Name every alias the target program
accepts, for example `args_any: ['--force|-f']`.

**`unless` is matched position-blind:** it checks whether an argument matches
*anywhere*, with no idea whether that argument is a standalone flag or another
option's value. `git clean -fdx -e --dry-run` satisfies `unless:
['--dry-run']` and is wrongly exempted — real git's `-e` consumes `--dry-run`
as its exclude-pattern argument, so this is not a dry run, it deletes for
real. There's no clean fix without per-program knowledge of which flags take a
following value, which ltk deliberately doesn't carry. Write each exception as
tight as it really is, and for anything destructive prefer [`mode:
confirm`](#rule-mode) over `unless`: it requires the agent to deliberately
repeat the exact command, so an argument elsewhere can't silently wave a
dangerous command through.

### Portability across shells

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
The list is an unordered set, and there is no "all-POSIX" shorthand: list the
dialects explicitly (`shells: [bash, zsh, sh, mksh]`).

```yaml
- id: no-cmd-rmdir
  match:
    command: [rmdir]
    args_all: ['/s']       # cmd's recursive delete; /s is its switch
    shells: [cmd]
  message: "Don't recursively delete directories from the agent."
```

`shells` is a gate evaluated before the command patterns, so a rule whose shell
does not match is skipped outright:

| Command line | Resolved shell | Fires? | Why |
|---|---|---|---|
| `rmdir /s build` | cmd | yes | shell in list; `/s` is an option under cmd |
| `rmdir /s build` | bash | no | shell not in `[cmd]`, so the rule is skipped |
| `rmdir /something` | cmd | no | shell matches, but `/something` does not match the anchored `'/s'` |

A rule with only `shells` and no `command` matches every command under those
shells. Reach for it when a rule only classifies
correctly on certain dialects, or when it names a shell-specific builtin. Most
rules (`git`, `go`, `rm`) mean the same thing everywhere and should omit it.

The shell in question is the one ltk resolved for that command, not the one you
typed it in. Resolution takes the first of: the `--shell` override, the engine's
per-call hint (Claude's PowerShell tool means `pwsh`), `defaults.shell`, `$SHELL`,
then `bash`. A wrapped inner command is re-parsed under the inner shell, so
`pwsh -Command "..."` invoked from bash yields commands whose shell is `pwsh`.
When a rule mysteriously fails to fire, check the resolved shell first.

PowerShell is parsed by PowerShell itself: ltk runs `pwsh` (or `powershell`) from
`PATH` to parse the command. Where neither is installed, every PowerShell command,
including the inner command of a `pwsh -Command "…"` wrapper, is unparseable, and
`defaults.on_parse_error` decides it. With the default `allow`, PowerShell rules
never fire on such a machine. `ltk check` reports this as `"analyzed": false` with
the parse error.

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
satisfied, so it is rejected at load rather than left to mislead you. The
effective window may not exceed 30 seconds (`rules.MaxConfirmWindowSeconds`);
a longer one is refused at load, never silently shortened. The cap bounds how
long a confirmation can outlive its use: concurrent hook invocations can
resurrect a spent override, but never past its original window.

`confirm` is defeatable by design — the agent that produced the command can
reproduce it, and a repeat is faster than complying, which is exactly why an agent
reaches for it. A delay inverts that incentive. `delay_seconds` (or
`defaults.repeat_delay_seconds`) ignores the repeat until N seconds after the
first denial, then honors it up to the window, so the override lives in the band
`[delay, window]`. The delay must be shorter than the window. A repeat inside the
delay gets a sharper rebuke and does not reset the timer.

```yaml
- id: tests-via-task-runner
  match: { command: [go, test] }
  mode: confirm
  delay_seconds: 10   # ignore an immediate repeat; honor one after 10s
  window_seconds: 30  # …up to 30s after the first denial (the maximum)
  message: "Run tests through the task runner."
  suggest: "just test"
```

The delay does not make `confirm` a control; a determined agent can sleep and
retry. What it buys is behavioral. It removes the "bypass is quicker than
compliance" incentive and turns a reflexive retry into a deliberate wait. For
something that must not be overridable, use `mode: enable`.

## Matching file edits (`path_rules`)

Most rules guard shell commands. A file rule instead guards the agent's own
editing tools (Edit, Write, MultiEdit, NotebookEdit), which is useful for files
owned by a tool and not meant to be hand-edited. File rules live in their own
list, `path_rules:`, beside `rules:`; ids are unique across both.

```yaml
path_rules:
  - id: no-hand-edit-version
    match: { path: [VERSION] }
    message: "VERSION is managed by the release tool — use `just bump`, not a hand edit."
```

Unlike the command fields, `path` patterns are globs, not regexes
([doublestar](https://github.com/bmatcuk/doublestar#patterns) syntax: `*`, `?`,
`[…]`, `{a,b}`, and `**`, which spans directory separators). Backslashes are normalized to `/`
first. The editing tools always pass an absolute path, so each pattern is tried
three ways: against the file's basename, so `*.lock` catches `/proj/a/b/c.lock`;
against the full path, so an absolute pattern matches as written; and against the
full path with an implicit `**/` prefix, so `src/**/*.go` catches `/proj/src/a/b.go`
and `dist/*` catches `/proj/dist/x` but not the deeper `dist/x/y`.

A trailing slash is directory sugar. `path: [vendor/]` expands to `vendor/**` and
blocks every file under any `vendor` directory at any depth — this is how you
prohibit writes to a whole subtree.

```yaml
  - id: no-edit-vendored
    match: { path: [vendor/] }   # the whole subtree; same as vendor/**
    message: "vendor/ is generated by `go mod vendor` — don't hand-edit it."
```

The reserved pattern `@submodules` expands at evaluate time to a subtree for every
path in the repo's `.gitmodules`, so one rule blocks edits inside all submodules
without naming them and stays correct as they come and go. A submodule's working
tree is a separate repo pinned at a commit, so editing it from the superproject
is almost always a mistake: the change is not tracked where the agent expects.
With no `.gitmodules` it matches nothing.

```yaml
  - id: no-edit-submodules
    match: { path: ["@submodules"] }
    message: "This file is inside a git submodule — edit it there, not from the superproject."
```

A file rule's `match` carries only `path`; a command condition there, or a
`path` under `rules:`, is a load error. Everything else (`action`, `message`,
`suggest`, `mode`) carries over,
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
