# Rule reference

Rules live in a YAML file (see [`examples/rules.yaml`](../examples/rules.yaml)).
Every command an agent proposes is parsed into a command graph; **each** command
in that graph is tested against the rules **in order**, and the **first matching
`deny` rule wins**. Its `message` and `suggest` are returned to the model so it
can retry the right way.

"Each command in the graph" means a denied command is caught no matter how it is
wrapped — inside pipelines, `&&`/`||`/`;` sequences, subshells `( … )`, brace
groups, command substitution `$( … )` / backticks, process substitution
`<( … )`, assignments (`x=$(…)`), backgrounding (`&`), and `if`/`for`/`while`
bodies. Quoted text is *not* a command, so `echo "go test"` does not match a
`go test` rule. (See the nesting tests in `internal/app`.)

> **Read this once before writing rules.** ltk is a guardrail against
> *reflexive* mistakes — an agent reaching for the raw command out of habit —
> not a security boundary. Every escape hatch documented on this page
> (`unless`, `mode: confirm`, `defaults.on_parse_error: allow`) is **fail-open
> by design**: when ltk can't fully resolve a command, or an exception token is
> present, or a rule is deliberately soft, it lets the command through rather
> than blocking on uncertainty. That is a considered trade-off (see the
> project [README's "Scope" section](README.md#scope--read-this-first)), but it
> means every rule you write inherits the same posture: it redirects a
> cooperative agent, it does not stop a determined one. For hard guarantees,
> run the agent in a sandbox/container.

**One deliberate exception to fail-open: an unrecognized tool that matched the
installed hook is denied, not allowed.** ltk's gated-tool lists
(`claudeGatedTools`, `antigravityGatedTools`) are the single source of truth
for both the installed `PreToolUse` matcher and runtime tool-name recognition
— but a vendor-renamed tool can still fire the installed matcher while ltk's
exact-name recognition misses it (agy's matcher is a genuine unanchored regex
today; Claude Code's takes the same unanchored path once its matcher contains
a real regex metacharacter — see [ARCHITECTURE.md](ARCHITECTURE.md#gated-tools-an-unrecognized-tool-is-denied-not-allowed)
for the verified specifics of each). When that happens, ltk cannot read the
payload's fields at all, so no rule can be evaluated against it — the
difference between "this rule doesn't apply" and "nothing is even looking" is
not one this guard is willing to gloss over. So where every other uncertainty
here (an unparseable command, `unless`, `mode: confirm`) resolves to *allow*,
this one resolves to *deny*: the call is
refused with a reason naming the tool, and the fix is to add it to the
engine's gated tools and reinstall (`ltk manage install`). This is narrower
than it sounds — it only fires for a tool the installed matcher was already
told to watch, never for tools that don't match it at all (Read, Grep,
WebSearch, …), which stay fail-open as always.

```yaml
version: 1

defaults:
  shell: bash               # fallback dialect (see Shell resolution in ARCHITECTURE.md)
  on_parse_error: allow     # command couldn't be parsed at all → allow (fail-open) | deny
  repeat_window_seconds: 30 # window for `mode: confirm` rules (see Rule mode)
  repeat_delay_seconds: 10  # min wait before a confirm repeat counts (0 = none)

rules:
  - id: go-test-to-just            # required, unique
    match: { command: [go, test] } # see "Matching commands" below
    action: deny                   # deny (default) | allow
    message: "Use `just test`."    # shown to the model on deny
    suggest: "just test"           # replacement command; also shown on deny
    mode: enable                   # enable (default) | confirm | disable (see Rule mode)
```

Unknown YAML keys are rejected, so a typo (`programm:`) is an error, not a
silent no-op.

**A deny rule must carry a `message` or a `suggest`** (either alone is enough —
`suggest` renders as "Use instead: …"). Without one the hook response has no
`permissionDecisionReason` at all: the model is told `deny` with no reason and no
alternative, and simply retries. A rule that cannot say why is rejected at parse
time. `allow` rules and `mode: disable` rules are exempt — neither ever denies.

## Matching commands

The `match.command` pattern is the program plus its arguments. It can be written
as a string (split on whitespace) or a list (verbatim):

```yaml
match: { command: "go test" }     # ≡
match: { command: [go, test] }
```

Tokens are classified into two kinds. **This is the core of the model:**

| Kind | What it is | How it matches |
|---|---|---|
| **Program** | the first token | `argv[0]` exactly **or by basename** (so `/usr/local/go/bin/go` matches `go`) |
| **Positional** (operand) | a non-option token (subcommand: `test`, `commit`) | **order matters** — must appear in order (an ordered *subsequence*) among the command's non-option arguments |
| **Option** (flag) | any flag (`-c`, `-x`, `--no-cache`) | **order does not matter** — must appear somewhere in the arguments; never consumes a positional slot |

(These are the standard CLI terms: a positional is a POSIX *operand*; an option
is the flag kind, what `argparse`/`clap` print under "options".)

Trailing arguments are always allowed. A pattern array may **freely mix**
positionals and options in any list order — tokens are classified by kind, not
by where they sit in the list, so `[docker, --debug, build]` and
`[docker, build, --debug]` are equivalent.

### Why positional vs option

A subcommand's position is meaningful: `go test` and `go help test` are different
operations, so `test` is matched **positionally** (positionals must appear in the
given order among the non-option arguments). An option's position is not
meaningful: `sh -e -c …` and `sh -c -e …` are the same, so options are matched as
an **unordered set**. Options are skipped when locating positionals.

### Allow rules match more strictly than deny rules

This is the one place `action: allow` and `action: deny` (the default) behave
differently, and it matters: **deny** rules match positionals as an ordered
*subsequence*, so a leading option never hides a subcommand — including a
**value-taking** one whose value lands among the operands (the matcher can't
know which flags consume a word): `go --mod=mod test`, `git -C /repo push`,
and `docker --context prod build` all still match `[go, test]` / `[git,
push]` / `[docker, build]` as a **deny**. The trade-off is that a positional
can match a non-leading operand of the same spelling, which only ever
*widens* what a deny rule catches — fail-safe for a guard.

**Allow** rules instead match positionals as a **strict, position-anchored
prefix**: every positional must equal the operand at the same index, starting
at operand 0. Subsequence matching on an allow rule is fail-*open*, not
fail-safe — the exact inverse of the deny case — because it widens what gets
let through rather than what gets caught: `allow: [git, status]` would
otherwise match `git commit -m status --no-verify` (`status` is `-m`'s VALUE,
not the `git status` subcommand), silently clearing a command a `deny` rule
was there to catch. So an allow rule with a value-taking option ahead of its
subcommand — `docker --context prod build` — does **not** match
`command: [docker, build]` with `action: allow`; write it with `args_all`
instead, which is program-agnostic and position-insensitive:

```yaml
match: { command: [docker], args_all: [build] }
action: allow
```

### Examples

```yaml
match: { command: go }                              # any `go …`
match: { command: [go, test] }                      # `go test …`, `go --mod=mod test …`
match: { command: "sh -c" }                         # `sh -c …`, `sh -e -c …`  (NOT `sh -e`)
match: { command: [git, push, --force, --no-verify] } # those flags in ANY order after `push`
```

### No glob or wildcard in `match.command` — intentional, not a missing feature

`match.command` tokens are matched as **literal argv elements**, classified by
role (program/positional/option) — never as a glob, a regex, or a
whitespace-delimited pattern over the raw command text. This is deliberate, not
an oversight, and it should **not** be added: a wildcard over an unparsed
command line is exactly the footgun `sudoers` has carried for decades — a rule
like `alice ALL=(ALL) /usr/bin/vim *` reads as "vim only," but `*` matches any
sequence of words the shell hands it, so `sudo vim -- /bin/sh` (and a long list
of documented `vim`/`less`/`awk` escape tricks) matches it too, because the
wildcard has no idea it's supposed to be matching "arguments" rather than
"bytes." ltk's program/positional/option classification (see "Matching
commands" above) exists specifically to keep every token typed by its **role**,
never pattern-matched against untyped text — so there is no whitespace
wildcard for a smuggled subcommand to hide behind. If a rule needs "this
option's value can be anything," that's what `args_any`/`args_all` already are
— program-agnostic **set membership** over already-classified tokens, not a
pattern over raw text. `match.path`, by contrast, *is* full-glob (`*`, `**`,
`{a,b}`) — file paths are a different address space (no argument-smuggling
risk: a glob there can't reclassify what a token *is*), so the two are not
inconsistent.

### Refinements

Optional, program-agnostic conditions refine a `command` match (or stand on
their own). `args_any` and `args_all` are **positive** filters — the listed
tokens must be present:

```yaml
# A docker build that publishes: `docker build` (or `buildx`) carrying both
# --push and --tag.
match:
  command: docker
  args_any: [build, buildx]   # at least one present anywhere in args
  args_all: ["--push", "--tag"]   # all present anywhere in args
```

**`unless`** is the **negative** one: it lists exception tokens, and if the
command contains any of them the rule does **not** match. It reads as English —
"match `git clean` *unless* `--dry-run` is present." This is how you carve out
the read-only / safe form of a command you otherwise block:

```yaml
# Block destructive `git clean`, but allow the preview form.
match:
  command: [git, clean]
  unless: ["-n", "--dry-run"]   # `git clean -n` only previews — fine
```

```yaml
# Block creating tags, but allow read-only listing.
- id: no-git-tag
  match:
    command: [git, tag]
    unless: ["--list", "-l", "-n"]   # `git tag --list` is fine
  message: "Releases go through the pipeline, not a hand-cut `git tag`."
```

Other genuine `unless` cases: `rsync … unless: ["-n", "--dry-run"]`,
`make … unless: ["-n", "--dry-run"]`, `helm upgrade … unless: ["--dry-run"]`.
(Note `--dry-run` is **not** universal — `docker build`/`run`, for instance, have
no dry-run; only `docker compose` does. Use the flag the target command actually
supports.)

All argument conditions see bundled short options expanded (so `-n` in `unless`
matches `rm -rn` too), and they are checked after `command` — an `unless` hit on
a non-matching command is moot.

### `unless` is matched POSITION-BLIND — prefer `mode: confirm` for destructive rules

`unless` (like `args_any`/`args_all`) only asks "does this token appear
**anywhere** in the argument list" — it has no notion of which option a token
belongs to. It cannot tell an exception token that stands alone as its own flag
from the same text sitting there as **another option's VALUE**. Demonstrated:

```
git clean -fdx -e --dry-run
```

Against `match: { command: [git, clean], unless: ["-n", "--dry-run"] }`, ltk
allows this — `--dry-run` is present in argv, so the exception fires. But in
real `git clean`, `-e` takes an exclude-PATTERN argument, and `-e --dry-run`
means "exclude files named `--dry-run`" — the token is `-e`'s VALUE, not a
standalone `--dry-run` flag. This is **not** a dry run; it deletes for real. The
matcher has no per-program argument-arity table (which flags consume a
following word, and how many), so it cannot see this the way `git` itself does
— the same blind spot [sudoers has around argument
position](https://www.sudo.ws/security/advisories/bash_env/) in its command
matching. There is no clean general fix without carrying that per-program
knowledge, which ltk deliberately does not (see "Matching commands" above for
why `ltk` stays program-agnostic).

**Consequence for rule authors:** `unless` is fine for *convenience* — carving
out a genuinely low-stakes read-only form so the rule doesn't nag on it — but
for anything **destructive**, prefer `mode: confirm` (see [Rule
mode](#rule-mode)) over leaning on `unless` to "allow the safe form." `mode:
confirm` denies by default and only lets the *exact same command* back through
on a **deliberate, repeated** invocation — a spurious exception token elsewhere
in the argument list can't silently exempt a dangerous one. `unless` conditions
should still be narrow (a small, well-known set of read-only flags) and never
the *only* thing standing between an agent and a destructive command it's about
to run for real.

## Portability across shells

Flag syntax differs by shell, so the option/positional classification is
**shell-aware**. The shell is resolved per command (see ARCHITECTURE.md) and the
same rule works everywhere:

| Shell family | A token is an **option** (flag) when it… |
|---|---|
| sh, bash, zsh, mksh | starts with `-` (`-c`, `--no-cache`). A lone `-` is positional (stdin). |
| PowerShell (pwsh) | starts with `-` (`-Path`, `-Recurse`). |
| cmd.exe | starts with `/` (`/c`, `/s`) **or** `-`. |

The `cmd` distinction matters: under cmd, `/c` is a switch, but under a POSIX
shell `/usr/bin/x` is a path — so the same leading-`/` token is an option in one
and positional in the other.

### Restricting a rule to certain shells (`match.shells`)

By default a rule applies under **every** shell. `match.shells` narrows it to a
list of dialects: the rule is only considered when the command's resolved shell
(see [Shell resolution](ARCHITECTURE.md#shell-resolution)) is one of them. An
absent or empty `shells` means "all shells".

```yaml
match:
  command: [del, /q]      # cmd's delete; `/q` is an option only under cmd
  shells: [cmd]           # so scope the rule to cmd
```

Valid entries are `sh`, `bash`, `zsh`, `mksh`, `pwsh`, and `cmd`; an unknown
shell is a config error (caught at load, like any other typo). The list is an
unordered set — `[cmd, pwsh]` and `[pwsh, cmd]` are identical.

Reach for `shells` when a rule is only meaningful, or only classifies correctly,
on certain dialects:

- **Flag syntax that only parses one way on the target shell** — the leading-`/`
  case above. `[robocopy, /mir], shells: [cmd]` matches `/mir` as an option; on a
  POSIX shell `/mir` would be read as a path operand and the rule wouldn't fire
  as intended.
- **Shell-specific builtins or cmdlets** — e.g. a PowerShell `Invoke-WebRequest`
  rule (`shells: [pwsh]`) that has no bearing on bash.
- **Platform-scoped policy** — a rule you only want to enforce where a given
  shell is in use.

If a rule is shell-agnostic (most are — `git`, `go`, `rm` mean the same thing
everywhere), omit `shells` so it covers them all.

#### How it combines with the rest of the match

`shells` is one condition in the `match` block, and **all** conditions in a
`match` must hold (logical AND). So `shells` acts as a gate evaluated *before*
the command pattern: if the resolved shell isn't in the list, the rule is skipped
outright and `command`/`args_any`/`args_all` are never even tested. A rule with
*only* `shells` and no `command` matches every command under those shells — which
is occasionally what you want (a blanket "this rule set doesn't apply on
Windows-`cmd`" guard), but usually you pair it with a `command`.

#### Which shell is "the resolved shell"

`shells` is checked against the single shell ltk resolved for the whole command
line, not per-token or per-program. That shell comes from the resolution
precedence in [ARCHITECTURE.md](ARCHITECTURE.md#shell-resolution) (force flag →
engine/tool hint → `defaults.shell` → `$SHELL` → `bash`). Two consequences worth
internalizing:

- A wrapped inner command is re-parsed under the *inner* shell. So
  `pwsh -Command "..."` run from bash yields nested commands whose shell is
  `pwsh`; a `shells: [pwsh]` rule will match them even though the outer line was
  bash. You scope to where the command actually runs, not where it was typed.
- If resolution lands on the wrong dialect (e.g. nothing hinted the shell and it
  fell back to `bash` on a Windows box), a `shells: [cmd]` rule won't fire. When
  a rule mysteriously doesn't match, check the resolved shell first.

#### Worked example

```yaml
rules:
  - id: no-cmd-rmdir
    match:
      command: [rmdir, /s]    # cmd's recursive dir delete; /s is its switch
      shells: [cmd]
    message: "Don't recursively delete directories from the agent."
```

| Command line | Resolved shell | Fires? | Why |
|---|---|---|---|
| `rmdir /s build` | cmd | ✅ | shell in list; `/s` classifies as an option under cmd |
| `rmdir /s build` | bash | ❌ | shell not in `[cmd]` — rule skipped before matching |
| `rmdir /something` | cmd | ❌ | shell matches, but `/something` ≠ the `/s` option |

The second row is the whole point: the *same text* is correct to block under cmd
and meaningless (a path operand) under a POSIX shell, so the rule is deliberately
scoped to where it parses correctly.

#### Multi-shell rules

List more than one dialect when a rule applies to a family but not all:

```yaml
match:
  command: [curl]
  shells: [bash, zsh, sh, mksh]   # POSIX shells only; skip pwsh/cmd
```

There is no wildcard and no "all-POSIX" shorthand — list the dialects
explicitly. Omitting `shells` entirely is the only "every shell" form.

**Bundled short options.** Under a POSIX shell, a single-dash cluster like `-rf`
is matched as if it also carried `-r` and `-f` separately (the getopt
convention), so `match: { command: rm, args_all: [-r, -f] }` catches `rm -rf`,
`rm -fr`, and `rm -r -f` alike. This is a matcher-only heuristic — the command
itself is never rewritten. Only POSIX shells expand this way: cmd (`/switch`)
and PowerShell (`-LongName`) don't bundle, so their tokens are never split.
(Why this lives in ltk and not the shell parser: bundling is a per-program
getopt convention — Go's `flag`, `find`, and `dd` don't follow it — so a shell
parser can't know to split it.)

**Short ALIASES are a different thing from bundled clusters, and are not
expanded.** `-rf` expanding to carry `-r`/`-f` works because those are the same
flags spelled together; it does **not** mean ltk knows `-f` is short for
`--force`, or `-n` for `--dry-run` — that mapping is entirely per-program and
ltk has no table of it. A rule written with only the long form misses the short
one: `match: { command: [git, push, --force] }` does **not** catch `git push
-f`, because `-f` never appears anywhere in that pattern. List every alias the
target program accepts explicitly — `args_any: ["--force", "-f"]` — the same
way the shipped `no-force-push` default does (see
[DEFAULTS.md](DEFAULTS.md#dont-bypass-the-gate)). This is a rule-authoring gap
to watch for, not an engine bug: nothing about the matcher is wrong, the rule
just didn't list what it meant to catch.

## Understanding (catching trivial workarounds)

We don't block on "scary" constructs — we **understand** them. Before matching,
a command is resolved as far as is statically possible, so an LLM can't sneak a
denied command past a rule with a trivial wrapper or a variable:

- **Variable resolution (shell).** Variable dereferences are expanded against the
  process environment (the hook inherits the callee's env) plus assignments seen
  earlier in the same command. So `t=test; go $t` and `CMD="go test"; bash -c
  "$CMD"` match the `go test` rule. Values we can't know (command output, `$1`)
  expand to empty and are simply not matched.
- **Wrapper re-parsing.** The inner command of a trivial wrapper —
  `bash -c "…"`, `sh -c "…"`, `eval "…"`, `cmd /c "…"`, `pwsh -Command "…"` — is
  re-parsed and matched, so the stated replacement still holds.

This is **not** a security boundary. If an LLM is told to work around a rule it
can rewrite the tool, recompile it under another name, symlink it, etc. — see
the README "Scope" section. For hard limits, run the agent in a sandbox.

## Rule mode

Every rule has a `mode` (default `enable`) that decides how firmly a `deny`
holds:

| mode | behavior |
|---|---|
| `enable` (default) | **Inviolate.** The denial is firm; nothing the agent does in-band lifts it. Use it for anything that must hold. |
| `confirm` | **Soft, repeat-to-override.** The first attempt is denied with a hint; re-running the *exact* same command (or re-attempting the same file edit) within the window is then allowed. An escape hatch for a human or a deliberate agent — **not** a control. |
| `disable` | The rule stays in the file but never matches. |

The confirm window comes from `defaults.repeat_window_seconds`, or a per-rule
`window_seconds`. A confirm rule with no window is inert (it can never be
satisfied, so it never fires).

### The confirm delay (`delay_seconds`)

`confirm` is, by design, defeatable — the agent that produced the command can
reproduce it. A repeat is *faster* than complying, which is exactly why an agent
reaches for it. A **delay** inverts that incentive: the repeat is **ignored until
N seconds after the first denial**, then honored up to the window — so the
override lives in the band `[delay, window]` (the delay must be shorter than the
window). The override message is also deliberately stern, and an over-eager
repeat *inside* the delay gets a sharper rebuke without resetting the timer.

The delay comes from `defaults.repeat_delay_seconds` (applies to every confirm
rule), or a per-rule `delay_seconds` that overrides it. The shipped defaults set
`repeat_delay_seconds: 10`.

```yaml
- id: tests-via-task-runner
  match: { command: [go, test] }
  mode: confirm
  delay_seconds: 10        # ignore an immediate repeat; honor one after 10s
  window_seconds: 60       # …up to 60s after the first denial
  message: "Run tests through the task runner."
  suggest: "just test"
```

This does **not** make `confirm` a control — a determined agent can `sleep` and
retry. What it buys is behavioral: it removes the "bypass is quicker than
compliance" incentive that drives most reflexive overrides, and it forces an
immediate retry to become a deliberate, visible wait. For something that must
*not* be overridable at all, use `mode: enable` (or run the agent in a sandbox).

## Matching file edits (`match.path`)

Most rules guard shell **commands**. A rule can instead guard **file edits** by
the agent's own editing tools (Edit, Write, MultiEdit, NotebookEdit) using
`match.path` — useful for files that are owned by a tool and shouldn't be
hand-edited:

```yaml
# Block hand-editing the version file; it's owned by the release tool.
- id: no-hand-edit-version
  match: { path: [VERSION] }
  message: "VERSION is managed by the release tool — use `just bump`, not a hand edit."
```

`path` patterns are full globs ([doublestar][ds]): `*`, `?`, `[…]`, `{a,b}`, and
`**`, which (unlike `*`) spans directory separators. Backslashes are normalized
to `/` first. Because the editing tools always pass an **absolute** path, each
pattern is tried three ways so a repo-relative pattern still fires:

- against the file's **basename** — `*.lock` catches `/proj/a/b/c.lock`;
- against the **full path** — an absolute or exact pattern matches as written;
- against the full path with an **implicit `**/` prefix** — so `src/**/*.go`
  catches `/proj/src/a/b.go`, and `dist/*` catches `/proj/dist/x` (but not the
  deeper `dist/x/y`, since `*` stops at `/`).

[ds]: https://github.com/bmatcuk/doublestar#patterns

### Blocking a whole directory

A trailing slash is directory sugar: `path: [vendor/]` blocks **every** file
under any `vendor` directory, at any depth (it expands to `vendor/**`). This is
how you "prohibit writes to a directory":

```yaml
- id: no-edit-vendored
  match: { path: [vendor/] }   # the whole subtree; same as vendor/**
  message: "vendor/ is generated by `go mod vendor` — don't hand-edit it."
```

### Blocking all git submodules (`@submodules`)

The reserved pattern `@submodules` expands, at evaluate time, to a directory
subtree for **every** path listed in the repo's `.gitmodules` — so one rule
blocks edits inside all submodules without naming them, and stays correct as
submodules are added or removed. A submodule's working tree is a separate repo
pinned at a commit; editing its files from the superproject is almost always a
mistake (the change isn't tracked where the agent expects). With no `.gitmodules`
the sentinel matches nothing.

```yaml
- id: no-edit-submodules
  match: { path: ["@submodules"] }
  message: "This file is inside a git submodule (a pinned, separate repo) — edit it there, not from the superproject."
```

A rule is **either** a command rule **or** a path rule, never both: combining
`path` with `command`/`args_*`/`shells` is a config error. Everything else
carries over unchanged — `mode` (enable/confirm/disable), `message`, and
`suggest` all work, and a `confirm` path rule is satisfied by re-attempting the
same file edit within the window.

> The hook must be registered for the editing tools for this to fire. `ltk
> manage install` registers the matcher `Bash|PowerShell|Edit|Write|MultiEdit|NotebookEdit`;
> a hook scoped to only `Bash` won't see file edits.
