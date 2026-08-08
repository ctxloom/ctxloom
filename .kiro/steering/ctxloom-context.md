---
inclusion: always
---

# Configuration precedence

Every binary in the ctxloom family (ctxloom, taskloom, ltk) resolves
configuration through one shared chain, owned by
`internal/shared/confload`:

    home config file  <  project config file  <  ENV VARS  <  --config-set FLAGS

Later layers win. `confload` reads and deep-merges the file layers and
resolves both override channels against the merged result, so there is
exactly one place this ordering is implemented — do not re-derive it in
a command.

## The two override channels

**Environment** — a dedicated namespace under the product's env prefix
(`CTXLOOM_CONFIG_`), e.g. `CTXLOOM_CONFIG_LLM_DEFAULTS_PRIMARY=big`.

**`--config-set`** — a repeatable root flag taking `<dotted.path>=<value>`,
e.g. `--config-set agents.MyCoder.runtime=container`.

## A command's own flags are NOT a config source

`--config-set` is the ONLY flag channel. `confload` deliberately does not
treat a changed flag's NAME as a config path, and this is load-bearing
rather than stylistic — an earlier revision did exactly that, and it
silently coupled every current and future flag name to the config schema.
Two failures were confirmed in production:

- `ctxloom agent set coder --runtime container` clobbered the project's
  top-level `runtime` key, because `--runtime` resolved as a config path.
- `--format json` on a structured-output command printed a warning line
  into what a script expected to be pure JSON, because `--format`
  resolved as "unrecognized config key, setting it anyway".

So when you add a flag, it configures THAT invocation. If the value
belongs in config, give it a schema path and let the chain resolve it;
never bridge a flag name to a config key by convention.

## Case handling diverges between the two channels, deliberately

A shell destroys an env var name's case before Go sees it
(`CTXLOOM_CONFIG_AGENTS_MYCODER_RUNTIME` cannot say whether the author
wrote `MyCoder`, `mycoder`, or `MYCODER`). A `--config-set` value never
passes through those rules, so it preserves exactly what was typed.

Both resolve identically when the target already exists or is a fixed,
canonically-cased schema field: case-insensitive match, adopting the
existing casing. They diverge only at a dynamic level that nothing
existing covers — an agent label, an LLM config label. There, env falls
back to whatever the shell handed over, while `--config-set` preserves
the typed case. That is why `--config-set` can mint a brand-new
case-sensitive key (`agents.MyCoder.runtime=container`,
`llm.configs.big.env.GEMINI_API_KEY=...`) and env fundamentally cannot.

---

# communication

## Do

- **State limitations immediately**: "Cannot verify X without Y", "Has limitation Z", "Need clarification on A"
- **Admit uncertainty**: "I don't know" is valid; label verified vs inferred; read/look up before asserting or changing
- **Ask for clarification when**: requirements ambiguous, multiple approaches exist, trade-off input needed, context uncertain
- **Lead with key info**: most important point, supporting details, rationale
- **Cite sources**: API docs, best practices, performance/security claims
- **Test before complete**: TDD mandatory—verify tests pass

## Don't

- **No sycophancy/politeness**: no praise, enthusiasm, validation seeking, or excessive courtesy
- **No assumptions**: ask rather than guess; explicitly state educated guesses

## Presenting decisions

- **Use the question system for choices**: put decisions through the interactive question tool, not prose; narrative is for the reasoning that feeds a choice, not the choice
- **Standalone-complete across time**: the user context-switches and may not recall an earlier decision or an hour-ago dispatch — restate the situation/behavior/stakes a question needs from earlier; within-batch shared context is fine, elapsed time is the gap
- **Batch by context, not count**: large batches OK if each question is via the question system AND individually standalone-complete; collapse dependents (if X settles Y, don't ask Y)
- **Recommend, don't survey**: lead with your recommendation as the first marked option; options mutually exclusive, each with its trade-off — a real fork

---

# Problem Solving

Find the root cause first; fix problems at their source. Never create workarounds without asking. If a simple problem seems to need a complex fix, stop and ask. When asking, present options — proper fix (effort estimate), workaround (trade-offs), temporary test disable (why), alternatives — with cost/benefit (tech debt, maintainability, time), and document the decision taken.

---

# Pushback

## Situations

- Skip tests
- Add features without tests
- Ignore type hints
- Work around linting

## Response

1. Why problematic
2. Consequences
3. Correct approach
4. Defer if insisted; note debt

## Feature Questions

- Acceptance criteria?
- Performance requirements?
- Error cases?
- Security?
- Logging?
- Dependencies?
- Testing?
- Error messages?

## Component Questions

- Dependencies?
- Dependency interface/protocol?
- Logging context?
- Error conditions & messages?

---

# Code Quality

Before writing: search the codebase for existing implementations; reuse or extend rather than duplicate or recreate.

Size: <500 lines per file (exceed only with very high coupling/cohesion); small single-purpose functions; optimize for reading, not performance; separate interfaces from implementations by file.

Naming: the interface is the thing — UserService is the Protocol, never IWhatever; implementations are named for how they implement: DefaultUserService (single), HttpUserService/CachedUserService (multiple).

Clean up: kill background processes when done; remove unused code, files, imports, variables; no dead code.

Comments explain why only. No change-tracking comments, no revision history in code, no commented-out code — git has it.

---

# ltk

**llm-tool-killer (ltk)** — pre-tool hook that inspects and redirects shell commands per `.ltk/config.yaml` rules.

## How it works

Parses real command (resolving variables, unwrapping wrappers) → matches against project rules → first matching `deny` returns `message`/`suggest`:

    go test ./...   →   blocked: "Run tests through the task runner."
                    →   retry: `just test`

## How to use

- Treat redirects as guidance; read suggestion and retry as specified
- Prefer project task runner (`just <target>`) over invoking tools directly  
- **Agents do not cut releases** — ltk blocks `git tag`/release commands; prepare version bump/PR for human or CI

## What it is not

Cooperative redirect, not a sandbox. Explicit workarounds are possible; for strict boundaries use a container.

---

AI code makes ~2x more concurrency mistakes than human code (CodeRabbit 2025, 470 PRs). Never parallelize sequential awaits; mark load-bearing ordering with `// SYNC-REQUIRED: [reason]`.

---

CLAUDE.md: 100-200 lines max, overflow to per-folder files. Include: tech stack with versions, architecture (folder purposes), build/test/lint/deploy commands, project-specific rules AI would otherwise violate. Exclude: language syntax, linter-enforced patterns, anything Claude gets right unprompted. Test each line: would Claude err without it? No → delete. When Claude errs, add the correction immediately.

---

AI code: correct happy path, dangerous elsewhere. LLMs hallucinate packages at 5.2-21.7% rates (arXiv:2406.10279) — verify imports (`npm info`/`pip show`/`cargo search`), API signatures, endpoints. Test integrity: AI "fixes" by deleting tests, removing assertions, mocking away behavior (mutation testing catches this). Red flags: unexplained deletions, catch-alls replacing specific handlers, removed validation, async/sync flips, unjustified deps. Reject immediately: security vulns in sensitive code, deleted tests, hallucinated deps, race conditions, missing error handling in critical paths.

---

# is-this-a-standard

# Is This a Standard?

Before writing infrastructure, ask:

> Is this a standard? Did we already wire it in?

## Why

LLMs default to local construction. They've seen `grpc.reflection.v1.ServerReflection` a thousand times, but write end-to-end over looking up.

## Common Categories AIs Reinvent

- gRPC Server Reflection (`grpc.reflection.v1.ServerReflection`)
- OpenTelemetry instrumentation (OTLP exporters)
- JWT validation (vetted library)
- OAuth 2.0 Device Authorization Grant (library)
- JSON Schema validation (metaschema)
- Kubernetes AdmissionReview (standard webhook shape)
- Conventional Commits parsing (existing parser)
- Healthchecks (`grpc.health.v1.Health`)

## What to Do

When AI proposes infrastructure:

1. Name the category
2. Ask if a standard exists
3. If yes, ask if project already wires it in
4. Use the standard

## Load-Bearing Comments

At registration site, name the invariant (see sibling `no-provenance-comments`):

```go
// reflection.Register: canonical "what does this server speak"
// mechanism. Do not add a parallel descriptor service; ask reflection.
reflection.Register(grpcServer)
```

Lives next to the call that triggers reinvention. No external doc dependency.

---

Mutation testing is the deterministic check on test quality; LLM tests look plausible while hiding tautological assertions, boundary gaps, and implementation coupling. After the TDD round-trip: run mutation testing, analyze survivors, add tests that kill them or accept the gap explicitly. Tools: cargo-mutants (Rust), pitest (Java), mutmut/cosmic-ray (Python), stryker (JS/TS), gomutation/go-mutesting (Go). Kill-rate targets: 80-90% pure functions and business logic, 60-70% framework glue, logging-only paths exempt. Coverage measures execution, mutation measures verification (80-90% coverage teams routinely see 30% kill rates). Never game the score with adjacent `assert!(true)`.

---

# no-provenance-comments

# No Provenance in Comments

Comments state invariants. Git records timeline.

## The Rule

No git-provenance metadata in code comments or codebase prose:

- Commit hashes (`see commit abc123`)
- Milestone tags (`wired in P0.2`)
- PR numbers (`added in #1234`)
- Release versions as historical markers (`introduced in 1.4.0`)
- Author/date stamps (`added by Alice 2024-01-15`)
- TODO sprint/iteration references

## Why

- Commit hashes break after rebase/squash
- Milestone tags become noise post-project
- Author/date stamps age into tombstones surviving author departure and line rewrites
- `git blame`/`git log` already record this; duplication guarantees divergence

## What to Write Instead

State the load-bearing invariant. If the comment survives a history rewrite, keep it. Else it's a timeline entry in the wrong file.

Before:
```go
// reflection.Register: wired in P0.2 (commit abc123). Canonical.
```

After:
```go
// reflection.Register: canonical "what does this server speak"
// mechanism. Do not add a parallel descriptor service.
```

## Where Provenance Lives

- `git blame`/`git log`
- Commit message body, PR description
- `CHANGELOG.md`/release notes
- ADRs
- Issue tracker

## Exceptions (Not Provenance)

- External stable IDs: RFC numbers, CVE IDs, language-spec versions
- Public-API `@since` annotations: version is part of API contract
- "We tried X and it failed": invariant is the rejected approach; dated artifact lives in rationale

---

# TDD for LLM-Written Code

LLMs non-deterministic; tests are deterministic gate.

## Rule
Demand TDD: tests first, implementation second. Non-negotiable.

## Workflow
1. Describe requirement
2. LLM writes tests against requirement (not implementation)
3. Review tests: requirement, edge cases, failure modes captured?
4. LLM implements to pass tests
5. Run tests

## Why
Test suite = contract. Without tests-first, model fills contract with its own output — no separate ground truth.

## Anti-Patterns
- Implementation-first, then tests → tests confirm bug, not requirement
- Vacuous assertions (`assert!(true)`)
- Implementation-coupled tests (`assert_eq!(hash("x"), 0x7a3f...)`) — brittle, blind to behavior
- Skipping step 3: test review is load-bearing

## What Tests Document
Document **problem**, not solution. Name requirement in test name; assert observable behavior.

## Verification
```
Show me the tests before the implementation. I will review the tests, approve them, then you write the implementation.
```

---

Never bake versions into AI context files; lockfiles are the source of truth (a static table ages out the moment the project moves). On session start read the lockfile and language-pin files; propose upgrades through the lockfile, never as one-offs in code. Verification prompt: "Your suggestion uses [pattern]. Project lockfile pins [package] at [version]. Confirm the pattern is supported at that version, or propose updating the lockfile."

---

# Warning Suppression
Do not suppress lint or compiler warnings without explicit user approval.  Warnings are signals of potential issues. Suppressing them without review risks hiding real problems.
Ensure that, when the user does approve a warning suppression, the comment includes the justification and details of the warning being suppressed. This creates a record for future reviewers to understand the context and reasoning behind the suppression.

---

# Gherkin

Business-readable spec, not test code: what and why, never how. Litmus test: "Will this wording change if the implementation changes?" — if yes, abstract to behavior.

Every feature opens with a preamble stating what the capability enables, why it matters to the business, and what breaks without it.

---

# Mutation Testing

High coverage + low mutation kill rate = false confidence.

```bash
git worktree add --detach ../.mutants-worktree HEAD
cargo mutants -d ../.mutants-worktree --in-place --timeout 120 -f <file> -- --lib
git worktree remove ../.mutants-worktree --force
```

Worktree shares .git and copies only source; `--in-place` is safe because the worktree is disposable.

Kill-rate targets: pure utilities/validators 90%+; business logic/state machines 85%+; orchestration/coordinators 70%+; framework glue/adapters 50%+. Skip generated code (`*.pb.rs`, `src/proto/`), trivial delegation, framework boilerplate.

---

# TDD

Red-green-refactor is mandatory; verify the red test runs and fails for the right reason before implementing.

Integration/acceptance: build and run actual binaries, don't define hooks; tag slow tests; isolate and clean up after yourself.

Naming: `test_<action>_<condition>_<expected_result>` in the language's casing; readability over strict format. Order test files by complexity — usage-demonstrating examples at top, edge cases at bottom; tests are documentation.

---

# Test Coverage

Target 90%+ (unit + integration, full application). Acceptable gaps: main entrypoints (E2E-covered), generated code, impossible panic/fatal paths, default factory functions (exclude via tooling).

---

# Test Organization

Tests live next to the code — same directory, separate clearly-named file (`.test.rs`, `_test.go`); not inline (contra the Rust default), not a parallel tree.

Rust wiring (test module vanishes from release builds):

```rust
#[cfg(test)]
#[path = "correlation.test.rs"]
mod correlation_tests;
```

Location by type: unit and BIT (Behavioral Interface Test — implementation against its interface's behavioral contract) in the adjacent `.test` file; integration (multiple components) in `tests/`; E2E in a separate test project; shared fixtures in `src/test_utils/`, not a parallel tree. Colocation is the default — don't separate without reason.

---

# Git

Branching: no branches or PRs unless explicitly asked; short descriptive names; one branch per task; branch off main/master only — push back and confirm anything else.

Commits: terse, describe code changes only, no meta-commentary; NEVER mention Claude, Anthropic, AI, or "Generated with".

Breaking changes: <1.0 and new major versions need no backwards compat — remove deprecated code immediately; post-1.0 minor/patch, discuss before implementing.

Pre-commit: lint, format, test before committing — never commit broken code; fix pre-commit errors automatically without asking. Bypass hooks (`--no-verify`) only for WIP on feature branches, with documented reasoning.

---

Go: testify/assert; fakes over mocks; no init() (explicit initialization); no package-level vars (DI); slog structured logging.

---

# Golang Dev

## Tools
- Acceptance: godog (Gherkin)
- Lint: golangci-lint, gofmt/goimports
- Logging: zap

## Test Layout
- Unit: `*_test.go` (co-located)
- Integration: `tests/integration/` or build tags
- Acceptance: `tests/acceptance/features/*.feature`
- Testify suites for shared setup; gomock via just target

## Constants
```go
// logmsg/messages.go
const UserCreated = "user_created"
logger.Info(logmsg.UserCreated, zap.String("username", username))

// errmsg/messages.go
const DivideByZero = "cannot divide by zero"
return 0, errors.New(errmsg.DivideByZero)
```

## IoC
```go
func NewUserService(repo UserRepository, logger *zap.Logger) *UserService {
    return &UserService{repo: repo, logger: logger}
}
func NewUserServiceDefault(db *Database) *UserService {  // nolint:unused
    return NewUserService(NewSQLUserRepository(db), zap.NewProduction())
}
```

---

# Go Testing

Name test functions TestFunctionName_Scenario_ExpectedResult; use descriptive subtest names.

---

# Repository and Worktree Layout

**Primary checkout** — leaf must be project name:
```
~/workspace/<project>
```

**Worktrees** — flat structure outside every repo:
```
~/workspace/worktrees/<project>--<branch-slug>    # feature/auth → feature-auth
```

Leaf directory carries both project + branch; visible in tooling. Slugify `/` → `-` for directory names only.

**Common Mistakes:**
- Primary checkout named `main` — must name project
- Worktree inside or beside repo — place in root only
- Reusing removed worktree directory — run `git worktree prune` first

---

# Worktree Lifecycle

**One worktree = one branch = one merge.** Agents in one work unit take turns in the same worktree or return read-only patches.

Every agent plan gets a worktree.

## Commit always

**Loss prevented only by committing.**

- Commit at every checkpoint (WIP, red)
- `--no-verify` OK on work-unit branches
- Merge is quality gate; clean history on entry
- Dirty tree: `remove` refuses without `--force`
- Uncommitted work in deleted worktrees is lost forever

## Done = merged + removed + deleted

Done ≠ "merged"; done = **merged, worktree removed, branch deleted.**

Worktrees ARE the ledger of open work. Stop at merge → accumulation.

Verify integration against actual branch (often not `main`). Use `git cherry <integration-branch> <branch>` (−-prefix = already upstream).

## Never force, never adopt

- **Never `git worktree remove --force` or `git branch -D`** — destroys uncommitted work
- **Never remove worktrees you didn't create**
- **Reaping = TRIAGE:** merged & clean → remove; dirty/unmerged → report human
- **`.git` is SHARED** — `branch -D`, `remove`, `gc`, `reflog expire` hit whole repo

## Worktrees from harnesses

Typical issues:

- **Stale base:** branches from ancestor/`origin/HEAD` (missing unpushed commits). Pin base SHA; verify `git log -1`.
- **Placement:** `/tmp` or scratchpad (wiped without warning). Keep only as commits.
- **Auto-clean:** directory vanishes; branch survives. Must commit.
- **`git worktree list --porcelain`** = ground truth.

## Recovering deleted worktrees

Order: `git worktree list` → branch ref → `git reflog` → `git fsck --lost-found`.

Only uncommitted work is lost.

---

# just: Command Runner

Language-agnostic task runner. Define tasks (`just test`, `just lint`, `just build`) in a `justfile`.

## TOP: standard repo-root variable

Every justfile defines `TOP`:

```just
TOP := `git rev-parse --show-toplevel`
```

All paths relative to `TOP`. Non-negotiable. Hard-coded relative paths or `{{justfile_directory()}}` break when invoked from subdirs or composed by parent justfiles.

```just
TOP := `git rev-parse --show-toplevel`

build:
    cargo build --manifest-path {{TOP}}/Cargo.toml --release
```

## Local justfiles, composed at root

Place justfile next to code it manages. Compose via `mod`:

```just
# /justfile (root)
TOP := `git rev-parse --show-toplevel`

mod web   "{{TOP}}/web/justfile"
mod api   "{{TOP}}/api/justfile"
```

Each submodule defines own `TOP`, owns own recipes. Root: `just web build`. Inside `web/`: `just build`. DO NOT use monolithic root justfile.

## Recipe shape

- Used 3+ times: lift to target.
- Top of file: short comment (purpose, prerequisites, side effects).
- Args via `+ARGS` (preserved through delegation).

## Cross-platform

Prefer `[unix]` / `[windows]` attributes over parallel platform justfiles. Reserve parallel files (`platform_justfile` import) for differing recipe shapes.

```just
[unix]
clean:
    rm -rf {{TOP}}/target

[windows]
clean:
    Remove-Item -Recurse -Force {{TOP}}\target
```

## Anti-Patterns

- Hard-coded relative paths (`./src/...`): break under composition.
- `{{justfile_directory()}}` as `TOP` stand-in: scoped to local file, not composing parent.
- Monolithic root justfile.

For container-delegated recipes, see `just-container-overlay` fragment.

---

# just-container-overlay

# just: Container Overlay Pattern

Host justfile delegates to container; inside container, different justfile mounted over host's runs actual command. Same `just build` works both contexts. No duplicate target names, no `container-build` vs `build` split.

## Setup

```just
# Host /justfile
TOP := `git rev-parse --show-toplevel`

_run +ARGS:
    docker run --rm \
      -v {{TOP}}:/workspace \
      -v {{TOP}}/justfile.container:/workspace/justfile:ro \
      -w /workspace build-env:latest just {{ARGS}}

build: (_run "build")
test:  (_run "test")
```

```just
# /justfile.container
TOP := `git rev-parse --show-toplevel`

build:
    cargo build --manifest-path {{TOP}}/Cargo.toml --release

test:
    cargo test --manifest-path {{TOP}}/Cargo.toml
```

## Why it works

Load-bearing: `-v {{TOP}}/justfile.container:/workspace/justfile:ro`. Bind mount obscures host `justfile` with `justfile.container` inside container. Inside: `just build` → container recipe (cargo). On host: `just build` → delegation spawning container.

## Separation

- Host file: orchestrates container, never builds.
- Container file: builds, never knows about container.
- Build changes → container file only.
- Orchestration changes → host file only.

## Real-world

Angzarr uses this across coordinators (aggregate, saga, projector, process-manager, stream, log, grpc-gateway). Each has local `justfile` + `justfile.container`. Root composes via `mod`; `just aggregate build` from root and `just build` from coordinator dir or container all produce same result.

## Anti-Patterns

- `_run` running on host instead of container: defeats overlay.
- Container justfile re-invoking `docker run`: accidental docker-in-docker.
- Mounting container justfile read-write instead of `:ro`: container build edits host file.
- Arg-escaping ceremony: just's `+ARGS` preserves multi-word args through delegation without `$$`-escaping. Use it.

---

# lefthook

This project manages its git hooks with **lefthook**: the hooks
are declared in `lefthook.yml` at the repo root, and
`lefthook install` wires them into `.git/hooks`.

## How to work with it

- Hooks run automatically on the matching git action (pre-commit,
  pre-push, ...). Run a stage manually with
  `lefthook run pre-commit`.
- A failing hook is a FINDING, not an obstacle: read its output
  and fix the cause, then retry the commit. Never bypass with
  `git commit --no-verify` or `LEFTHOOK=0` unless the user
  explicitly tells you to.
- Add or change hooks by editing `lefthook.yml` (each entry is a
  named command with optional glob/exclude), then re-run
  `lefthook install`.

---

# reprise

This project runs **reprise**, duplicate detection built for
LLM-generated code (a *reprise* is a theme that returns in altered
form — a near-duplicate). LLM assistants systematically
reimplement existing helpers instead of calling them, and the
copies then drift apart; reprise catches clone Types 1-3 plus the
reimplemented-helper slice of Type-4, within this codebase. It is
REPORT-ONLY: it never edits code — responding to findings is your
job.

## The two commands

- `reprise scan` — full-repo ranked report of duplicate groups.
- `reprise check` — PR/commit mode: findings involving units
  changed since a git ref fail when they are new or worsened.
  The base ref defaults to the merge-base with the default
  branch; override with `--base <ref>` or `[baseline].ref` in
  `reprise.toml`. Its flagship finding is `inconsistent-update`:
  a change edited ONE copy of a known duplicate group but not the
  others. Exit codes: 0 clean, 1 findings at/above `--fail-on`,
  2 usage/runtime error.

There is no `baseline` subcommand: the baseline is a pinned git
ref, not a stored file. Adopt reprise on a legacy codebase by
pinning `[baseline].ref` to the current commit, so only NEW drift
fails while existing duplication is grandfathered.

## How to respond to findings

- **Before writing a helper**, search for an existing one and
  call it — that is the failure mode reprise exists to catch.
- **`inconsistent-update`**: the fix you just made belongs to
  every copy in the group. Prefer extracting the shared helper
  and calling it everywhere; at minimum, apply the change to all
  copies.
- **A new duplicate group**: replace your new implementation with
  a call to the existing unit (or extract one shared helper).
- **Intentional parallelism**: mark deliberate duplication at the
  source — `reprise:accept-drift` waives the drift gate while
  keeping the unit tracked; `reprise:ignore` drops the unit
  entirely. Never suppress a finding without the user's say-so.

## Pre-commit gate

reprise runs as a **lefthook pre-commit hook** (`reprise check`
in lefthook.yml). A failing hook means respond as above and
retry the commit — do not bypass with `--no-verify`.

---

Dev Containers: `.devcontainer` config for consistent dev env w/ tools, deps, system reqs. Reproducible builds.

---

# CI/CD: Use Just Targets

CI/CD invokes just targets for anything project-specific: build, test, lint, deploy, package, release, codegen — if a command encodes project info, wrap it in a just target. Only generic operations stay inline in workflow files: git operations, tool-setup/cache/artifact actions, env vars and secrets injection, exploratory debugging (ls/pwd/cat/echo).

---

# GitHub Release Pipeline (Master)

Every master push auto-releases: build-test (skipped on `[skip ci]`) → integration → mutation gate (fail if score < 60%) → bump-release (patch bump, `release/vX.Y.Z` branch, push `v*` tag) → tag triggers separate release.yml.

Key patterns: `[skip ci]` on version commits prevents infinite loops; `release/vX.Y.Z` branch naming; `v*` tag triggers release.yml; mutation threshold quality gate starts at 60%; dogfood the project's own version tool.

---

# A test does not pass until a mutation dies

A test is not passing because it is green. It is passing when it is
green AND a mutation to the production code it names makes it FAIL.
Green alone means the test ran. It does not mean the test looked.

Apply it in both directions:

- **Writing a test**: after it goes green, break the behaviour it
  names — in `internal/` or `cmd/`, not in the test — and confirm it
  goes red. Revert. If nothing you can break makes it fail, you have
  written a tautology, and it is worse than no test because it
  reports coverage you do not have.
- **Trusting a test**: a suite's green tells you nothing about a
  specific claim until someone has killed a mutation against it.
  "It passes" is not evidence. "It failed when I broke X" is.

## Why this is a rule here and not a preference

An audit of this project's acceptance suite on 2026-08-04 read 380
scenarios, ran 41 mutations, and found 25 assertions that passed
while proving nothing. Not edge cases — one asserted that the output
matched the regex `.` (any one character). Others asserted an
argument the command had echoed back, or a MIME type that is a
static field on the envelope, or the ABSENCE of something the
fixture never created. Several were satisfied by the subject never
running at all: a scenario titled "the same engines proceed" passed
when the engine never launched, and one titled "a per-item
acceptance and a rejection ARE RECORDED" passed with the record
store neutered to write nothing.

Every one of those had been green for as long as it had existed.

## The shapes that produce a false green

- asserting exit 0 without asserting the EFFECT (this project's
  characteristic silent no-op: exit 0, a success message, zero bytes)
- asserting a name, a header, or an argument the command echoes
- asserting a file exists without asserting its content or mode
- asserting the tool's REPORT of what it did instead of the state it
  changed
- absence-satisfies-absence: asserting something is missing in a
  fixture where it was never present
- a substring so generic it survives gutting the thing under test

## Where the bar is already met, and worth copying

`tests/acceptance/steps_skill.go` compares bytes with an explicit
zero-length guard — "comparing two empty reads is trivially
identical" — which is the line that stops a byte comparison from
being vacuous. `j26` asserts an exact file COUNT rather than
existence. `run.feature`'s recorded-input assertion reads what the
engine actually received rather than what the CLI said it sent.

## Test against a FRESH BUILD, never the installed binary

Any manual or agent-driven verification runs against a binary built
from the tree under test — `just build`, then `./ctxloom` — never
`ctxloom` from `$PATH`.

The installed binary is a different program. On 2026-08-05 a check
of whether a newly added fragment was delivered came back "missing";
the installed `ctxloom` was `v0.7.0-cefeb77-20260728T201548-dirty`,
eight days and ~150 commits behind the tree, and dirty on top. Built
from source, the same check passed. The measurement was wrong, not
the code — and a wrong measurement that agrees with your fear is the
expensive kind.

`just test-acceptance` already does this correctly: it depends on
`build` and drives the binary it just produced. The exposure is
hand-run commands and agent verification, which reach for `$PATH`
by default.

**`release/0.7` will not be installed** until every journey runs
with mutation testing validating the tests' quality. Until then,
`ctxloom` on `$PATH` is deliberately old, and anything it tells you
about this branch is a coincidence.

---

## Isolation: specify both axes

Creating, configuring, or delegating to a ctxloom agent (`ctxloom agent
set`, `run --agent`, `agent_run`)? Set both axes explicitly — never rely
on the default:

- **runtime** (`host`|`container`, the agent binding's `runtime:`)
  isolates the PROCESS.
- **workspace** (`none`|`worktree`, per-invocation `--workspace` /
  `agent_run`'s `workspace`) isolates the FILES.

They're independent: `container` can still mount the workspace at the
SAME absolute path as the live project (process isolated, edits still
land where the editor already looks); `worktree` still runs the engine
on the host (the editor goes blind to that tree by design — results
return via the delegated-agent merge flow, not live edits). Picking one
says nothing about the other.

Unspecified means `host`+`none` — isolated on NEITHER axis. That's a
default, not a decision.

Containers make isolation a property of the runtime, not a request to
the engine: some vendor CLIs ignore env-var isolation hints and write
credentials/state to a global path regardless.

A bad or missing agent name silently degrades to `host`+`none` with only
a stderr warning, discarding the runtime and permissions you asked for —
confirm the name resolves before trusting the isolation you requested.
