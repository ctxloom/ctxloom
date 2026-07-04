---
inclusion: always
---

# Go Project Structure

## Layout
```
cmd/           # Main apps
internal/      # Private code
pkg/           # Public libs
api/           # API defs (OpenAPI, proto)
web/           # Web assets
configs/       # Config templates
scripts/       # Build scripts
test/          # Integration tests
```

## Packages
- Small, focused
- No circular deps
- Internal for impl details
- Accept interfaces, return structs

## Deps
- go.mod for management
- Vendor for reproducibility
- Minimize external deps

---

# Go Testing

## Structure
- Table-driven tests w/ testify/assert
- t.Run for subtests

## Naming
- TestFunc_Scenario_Expected
- Descriptive subtest names

## Pattern
```go
tests := []struct{ name string; a,b,expected int }{...}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        assert.Equal(t, tt.expected, Add(tt.a, tt.b))
    })
}
```

## Coverage
- 80%+ on critical paths
- Test error cases
- Mock external deps

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

# Code Review — Conduct

Focused code reviewer for this lens.

## How to review
- Understand change, why it's done
- Judge principles, not style
- Cite `file:line`, explain concrete consequence ("leaks connection on error path")
- Prefer smallest correct fix
- Note good patterns

## Output (per finding)
- **Severity**: Critical (fix before merge) / Major (should fix) / Minor (nice to have)
- **Where**: `path:line`
- **What & why**: issue, concrete consequence
- **Fix**: suggested change

If change is clean, state it. Be honest about uncertainty; don't invent.

---

# Code Review — Synthesis

Synthesize multiple specialist reviews into one coherent report.

## Synthesis
- **De-duplicate**: collapse identical issues; note lenses flagging them
- **Resolve conflicts**: surface trade-offs, recommend
- **Rank** by severity (Critical → Major → Minor), confidence
- **Drop noise**: filter low-value/speculative items
- Preserve `file:line` refs, concrete fixes

## Output
1. **Summary**: what changed, assessment, merge recommendation (block / approve-with-changes / approve)
2. **Critical, Major, Minor**: `file:line`, consequence, fix, source lens(es)
3. **Notable strengths** (brief)

---

# Code Review — Thoroughness

Review the ACTUAL code. Do NOT judge from summaries, prior memories,
commit messages, or file names — those mislead. For every file in scope:

- OPEN and read the file (or the full diff hunk plus enough surrounding
  context to understand it), top to bottom. A name like `validate.go`
  or a summary like "adds validation" is a hypothesis, not evidence.
- Do not assume a function does what its name says — read its body.
- Do not skip a file because it "looks unrelated" or "looks generated" —
  confirm by reading. Name anything you deliberately skip, and why.
- Trace the change end to end: callers, callees, error paths, and the
  data it touches — not only the lines that changed.
- Trust what you have read this session over anything recalled from a
  previous one; re-read rather than rely on memory.

If the change is too large to read in full, say so explicitly and scope
exactly what you did and did not read — never imply coverage you did not do.

---

# Observability

- **Logging**: structured, correct levels, actionable; no secrets/PII; correlation/request IDs
- **Metrics**: key ops measured; bounded label cardinality
- **Tracing**: spans for significant ops; trace context propagated across boundaries
- **Security/audit logging**: auth events, access-control denials, privileged actions (OWASP A09:2025; logging without alerting = failure)

---

# communication

## Do

- **State limitations immediately**: "Cannot verify X without Y", "Has limitation Z", "Need clarification on A"
- **Ask for clarification when**: requirements ambiguous, multiple approaches exist, trade-off input needed, context uncertain
- **Lead with key info**: most important point, supporting details, rationale
- **Cite sources**: API docs, best practices, performance/security claims
- **Test before complete**: TDD mandatory—verify tests pass

## Don't

- **No sycophancy/politeness**: no praise, enthusiasm, validation seeking, or excessive courtesy
- **No assumptions**: ask rather than guess; explicitly state educated guesses

---

# problem-solving

**Core principle:** Find root cause before solution. Never workaround without investigation.

## When Encountering Failing Functionality

1. **Find root cause first** — investigate before creating workarounds
2. **Question complex fixes to simple problems** — ask before proceeding
3. **Prompt for guidance:**
   - Fix properly (estimate effort)
   - Workaround (document trade-offs)
   - Disable test temporarily (document why)
   - Alternative approaches
4. **Cost/benefit analysis:**
   - Pros/cons per option
   - Technical debt implications
   - Maintainability impact
   - Time investment
5. **Document reasoning** for chosen path

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

# Go Rules (Compressed)

## Error Handling
```go
// Add context: fmt.Errorf("fetch user %s: %w", userID, err)
// Use errors.Is()/errors.As() for checks
```

## Concurrency

### I/O funcs: context.Context first
```go
func FetchUser(ctx context.Context, id string) (*User, error)
```

### Goroutines need shutdown
```go
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return doWork(ctx) })
return g.Wait()
```

### Sync choice
- Simple state → `sync.Mutex`
- Read-heavy → `sync.RWMutex`
- One-time → `sync.Once`
- Communication → channels

## Interfaces
Define where used, not implemented:
```go
// In consumer pkg
type UserStore interface { GetUser(id string) (*User, error) }
```

## Avoid
- `init()` - explicit init
- Pkg-level vars - use DI
- `ioutil` - use `io`
- Channels for simple state

## Logging
```go
slog.Info("user created", slog.String("user_id", user.ID))
```

---

# code-quality

## Before Writing Code
- **Search** codebase for existing implementations before writing
- **Extend, don't recreate**: Refactor existing rather than duplicate
- **Reuse** functions, types, modules

## Code Size
- Files: <500 lines, small & focused
- Functions: One thing each
- Optimize for readability, not performance
- Separate interfaces from implementations

## Naming
- **Interfaces/Protocols**: UserService (not IUserService)
- **Implementations** named by how they work:
  - Single: DefaultUserService
  - Multiple: HttpUserService, CachedUserService

## Philosophy
- **SOLID**: Single responsibility, Open/closed, Liskov substitution, Interface segregation, Dependency inversion
- **KISS**: Clear, straightforward solutions
- **YAGNI**: Build only when needed

## General Principles
- Follow language style guides
- Type hints/annotations
- Self-documenting code > excessive comments
- Document public APIs
- Composition over inheritance

## Coupling & Cohesion
- **Low coupling**: Minimal dependencies
- **High cohesion**: Group related functionality
- Single, well-defined purpose per module/class

## Code Markers
- `TODO`: Work needed
- `FIXME`: Known bugs
- `NOTE`: Important info
- `HACK`: Works but needs improvement

## Performance
- Profile before optimizing
- Document requirements in tests
- Appropriate data structures
- State algorithmic complexity for non-trivial algorithms
- Consider memory allocation patterns

## Clean Up
- Kill background processes when done
- Remove unused: code, files, imports, variables
- No dead code

## Comments
- **Never** change-tracking comments (git tracks history)
- **No** revision history or commented-out code (delete; git preserves)
- Comments explain **why**, not what or changes

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

# Sequential Thinking MCP Server

Use the sequential thinking MCP server when facing complex problems that benefit
from structured, step-by-step reasoning.

## When to Use
- Multi-step problem solving
- Complex debugging scenarios
- Architectural decisions
- Trade-off analysis

## How It Works
The server provides tools to:
1. Break down problems into discrete steps
2. Track reasoning chains
3. Revisit and refine earlier conclusions
4. Build comprehensive solutions iteratively

## Best Practices
- Start with a clear problem statement
- Use for problems with 3+ logical steps
- Review and adjust intermediate conclusions
- Summarize the final reasoning chain

---

# concurrency-warnings

# Concurrency Warnings

AI code ~2x more likely to have concurrency/dependency bugs (CodeRabbit, *State of AI vs Human Code Generation*, 2025; 470 PRs, ~1.7x issues overall).

## Race Condition Reintroduction

AI may "optimize" sequential→parallel, reintroducing bugs.

```javascript
// SYNC-REQUIRED: email needs user.id from save
await saveUser(user);
await sendEmail(user);

// AI BUG: await Promise.all([saveUser, sendEmail])
```

**Protection:** Comment `// SYNC-REQUIRED: [reason]`

## Common Mistakes

### Go
- No shutdown: `go doWork()` → errgroup + context
- Channel overuse for state → mutex
- Missing ctx → always pass `context.Context`

### JavaScript/TypeScript
- Unhandled rejection → handle/propagate
- Listeners without cleanup → remove in cleanup

### Rust
- `std::thread::sleep` in async → `tokio::time::sleep`
- Unnecessary `Arc<Mutex>` → message passing

## Review Checklist
(See [[human-review-checklist]])
- Every goroutine/thread has shutdown path
- Context cancellation propagated
- Async errors handled
- No blocking in async
- Sequential code not parallelized

---

# Context Engineering

## CLAUDE.md Rules

**100-200 lines max.** Overflow → per-folder files.

### Must Include
- Tech stack w/ versions
- Architecture (dirs + purpose)
- Build/test/lint/deploy commands
- Project conventions AI would violate

### Exclude
- General syntax
- Linter-enforced patterns
- Things Claude does correctly

**Test:** Would Claude err without this? No → delete.

## Hierarchy
1. `~/.claude/CLAUDE.md` - User prefs
2. `CLAUDE.md` - Project root
3. `folder/CLAUDE.md` - Module-specific

## Prompt Patterns

- **Full context:** Include file + specific issue, not just "line 42"
- **Constraints:** "Optimize query, <100ms, no indexes"
- **Reference patterns:** "Add endpoint like /api/products/:id"

## Context Management

- `/clear` between unrelated tasks
- `/compact focus on [topic]` for targeted compaction
- Add standing instructions for compaction preservation

## Maintenance

Mistake → immediately add correction to CLAUDE.md.

---

# human-review-checklist

AI code: happy path correct, dangerous elsewhere.

## Security (Never Skip)
- Auth/authz correct
- Inputs sanitized
- Parameterized queries
- Output encoding
- No hardcoded secrets
- Deps exist & secure

**Always review:** auth, payments, user data, admin

## Hallucination Check

LLMs reference nonexistent packages: 5.2% (commercial) to 21.7% (open-source) (Spracklen et al., arXiv:2406.10279, 2024; 16 LLMs, 576k samples).

- Verify imports: `npm info`, `pip show`, `cargo search`
- Check API signatures
- Confirm endpoints exist

## Error Handling (AI omits)
- Null checks, early returns, exceptions, edge cases

## Test Integrity (AI "fixes" by)
- Deleting tests
- Removing assertions
- Mocking away behavior
- See `mutation-as-test-validator`

## Red Flags
- Unexplained deleted code
- Catch-all replacing specific handlers
- Removed validation
- async/sync changes w/o reason
- Unjustified new deps

## Reject Immediately
- Security vulns in sensitive code
- Deleted tests w/o justification
- Hallucinated deps
- Race conditions
- Missing error handling in critical paths

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

# Multi-Step Workflow

## Process
1. **Outline** - Describe approach before code
2. **Pseudocode** - Logic/signatures, no impl
3. **Generate** - Small testable pieces
4. **Integrate** - Wire up, full test suite

## Per-Stage
- **Outline**: Components, data flow, edge cases → approve before proceeding
- **Pseudocode**: Signatures + error paths, no impl
- **Generate**: One function + tests at a time
- **Integrate**: Follow existing patterns, run full suite

## Task Sizing
- Single-shot: one-line fix, boilerplate
- Multi-step: new features, architectural, multi-file, security-sensitive

## Iteration
Treat output as draft. Refine:
```
Add error handling for timeouts
Use slog logging
Add retry w/ exponential backoff
```

## Anti-Patterns
- Entire system in one prompt
- Skipping validation
- Not reading AI explanations

---

# mutation-as-test-validator

# Mutation Testing as Test Validator

Passing tests ≠ good tests. Mutation testing is the deterministic check.

## What It Does

1. Mutates source (flip `>` to `>=`, early return, change return value)
2. Runs test suite against each mutant
3. Reports survivors: mutants tests didn't kill

Surviving mutant = test suite accepts wrong code. Line covered, behavior unverified.

## Why It Matters for LLM-Written Tests

LLMs produce plausible tests at scale. Failure modes caught:

- **Tautological assertions**: `assert_eq!(result, calculate(5, 3))` always passes
- **Missing edge cases**: happy path only, boundary gaps
- **Implementation-coupled**: brittle to refactor, blind to real bugs

## Workflow Integration

After TDD round-trip (see [[tdd-for-llm]]):

1. LLM writes tests
2. You review tests
3. LLM implements
4. **Run mutation testing**
5. Analyze survivors
6. Add tests killing survivors, or accept gap explicitly

## Tools

- Rust: `cargo-mutants`
- Java: `pitest`
- Python: `mutmut`, `cosmic-ray`
- JS/TS: `stryker`
- Go: `gomutation`, `go-mutesting`

## Target Kill Rates

| Code shape | Target |
|---|---|
| Pure utility | 80-90%+ |
| Business logic | 80-90% |
| Framework glue → tested core | 60-70% |
| Logging-only paths | survivors OK |

Teams w/ 80-90% coverage often hit only 30% mutation kill on first adoption. Coverage = execution; mutation = verification.

## Anti-Patterns

- Treating coverage as test quality
- Killing mutants via `assert!(true)` adjacent to line — score up, signal zero

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

# version-source-of-truth

# Versions: Source of Truth

Do NOT bake version specs into AI context files. Project config + lockfiles are source of truth.

## Where Versions Live

- **Node**: `package.json` + `package-lock.json`/`yarn.lock`/`pnpm-lock.yaml`
- **Python**: `pyproject.toml` + `uv.lock`/`poetry.lock`/`requirements.txt`
- **Go**: `go.mod` + `go.sum`
- **Rust**: `Cargo.toml` + `Cargo.lock`
- **Java**: `pom.xml` or `build.gradle` + `gradle.lockfile`
- **Swift**: `Package.swift` + `Package.resolved`
- **Runtime pin**: `.nvmrc`, `.tool-versions`, `mise.toml`, `rust-toolchain.toml`

## Why Not a Static Table

Baked table ages out; lockfile is always current. Lockfile = contract; table = tombstone.

## AI Behavior

On session start, read lockfile + language-pin files. Use versions declared there. If feature needs newer version, propose bump via lockfile, not one-off in code.

## Verification

```
Your suggestion uses [pattern]. Project lockfile pins [package] at [version]. Confirm the pattern is supported at that version, or propose updating the lockfile.
```

---

# ai-developer

# AI Developer Guidelines

Behavioral guidelines to reduce common LLM coding mistakes.

## 1. Think Before Coding

State assumptions explicitly. If uncertain, ask. Present multiple interpretations—don't pick silently. Name confusion. Push back on unclear requirements.

## 2. Simplicity First

Minimum code that solves the problem. No speculative features, unnecessary abstractions, or error handling for impossible scenarios. Rewrite if overcomplicated.

## 3. Surgical Changes

Touch only what you must. Match existing style.

**When editing:**
- Don't improve adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Remove only imports/variables/functions YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

**Test:** Every changed line traces directly to the user's request.

## 4. Goal-Driven Execution

Define verifiable success criteria. Transform tasks:
- "Add validation" → Write tests for invalid inputs, make them pass
- "Fix the bug" → Write test reproducing it, make it pass
- "Refactor X" → Ensure tests pass before and after

State brief plans with steps and verification. Strong criteria enable independent looping.

---

# Gherkin Authoring

Business-readable spec, not test code. Describe **what/why**, never **how**.

**Litmus test:** "Will wording change if implementation changes?" If yes → abstract to behavior.

## Declarative > Imperative

```gherkin
# Wrong: UI choreography
When I click "Add to Cart"...

# Right: Business intent
When I purchase the items in my cart
```

## Given-When-Then

| Keyword | Purpose | Example |
|---------|---------|---------|
| Given | Past state/context | `Given a player with $500` |
| When | Single trigger | `When player reserves $200` |
| Then | Business outcomes | `Then available balance is $300` |

## Business Language

| Avoid | Prefer |
|-------|--------|
| API returns 201 | Order confirmed |
| DB has record | Customer exists |
| Event published | Notification sent |

Exception: Framework tests use technical vocab—it's their domain.

## Rules

- One scenario = one behavior
- Feature preambles: what/why/what breaks
- Error cases are first-class citizens

## Anti-Patterns

- UI steps: "click", "fill in", "navigate"
- Technical assertions: "database has row"
- Conditional logic (use separate scenarios)
- Vague outcomes: "works correctly"
- Hardcoded test data

## Cross-Domain

Show interactions explicitly, hide implementation:

```gherkin
When the order is completed
Then a fulfillment request is created with:
  | sku | quantity |
```

---

# Mutation Testing

Validate test quality by verifying tests catch code mutations. High coverage + low mutation kill rate = false confidence.

## Workflow

```bash
git worktree add --detach ../.mutants-worktree HEAD
cargo mutants -d ../.mutants-worktree --in-place --timeout 120 -f <file> -- --lib
git worktree remove ../.mutants-worktree --force
```

**Why worktree?** Shares .git (~300MB), copies only source (~10MB). `--in-place` safe since disposable.

## Target Kill Rates

| Code Type | Target |
|-----------|--------|
| Pure utils, validators | 90%+ |
| Business logic, state machines | 85%+ |
| Orchestration | 70%+ |
| Framework glue | 50%+ |

## Outcomes

| Outcome | Action |
|---------|--------|
| Caught | Good — test meaningful |
| Missed | Add/improve test |
| Timeout | Usually caught |
| Unviable | Type system wins |

## Survivors

1. **Missing test** — Write one
2. **Weak assertion** — Add assertions
3. **Equivalent mutation** — Accept or refactor
4. **Test isolation gap** — Verify integration exists

## Skip
- Generated code (`*.pb.rs`, `src/proto/`)
- Trivial delegation
- Framework boilerplate

## Anti-patterns
- Mutation testing without coverage first
- Chasing 100% kill rate
- Testing mutations in mocks
- Ignoring unviable mutations (these are wins)

---

# TDD Workflow

## Cycle (Mandatory)
1. **Red**: Write failing test first; verify fails correctly; ensure isolation
2. **Green**: Minimal code to pass; no extras
3. **Refactor**: Keep green; apply SOLID; remove duplication; use existing libs; improve names

## Integration Tests
- Run actual binaries (no hooks)
- Tag slow tests appropriately
- Clean up after tests

## Naming
`test_<action>_<condition>_<expected_result>`
- Python: snake_case
- C#/Java/JS: camelCase
- Readability > format

## Organization
- Prioritize readability
- Order by complexity: simple examples top, edge cases bottom
- Tests = documentation

---

# Test Coverage

Target: 90%+. Unit + integration covering full app.

## Approach

1. Analyze architecture, deps, patterns
2. Identify gaps via coverage tools
3. Refactor for testability (DI, interface extraction)
4. Write tests: business logic > error paths > integrations
5. Verify target met

## Acceptable Refactoring

- Extract interfaces from concrete deps
- Constructor injection
- Split large functions
- Create seams for external calls

## Scope

- Unit: all public functions/methods
- Integration: component interactions
- Testcontainers for external deps
- Mocks/fakes for isolation
- Edge cases, errors, happy paths

## Acceptable Gaps

- Main entrypoints (E2E covers)
- Generated code
- Unreachable panic/fatal
- Default factories (exclude via tooling)

## Documentation

Each test: **Why** (risk/behavior) + **What** (scenario). Code shows how.

## Design

- Tests = primary docs
- Clarity > cleverness
- One behavior/test
- See `tdd` for naming convention

## File Organization

Order: core happy path → variations → errors → edge cases → minutiae

## Mocking

**Mock:** external systems, network, FS, slow deps
**Real:** own code, stdlib, fast deterministic

See `test-organization` for detailed patterns.

## Avoid

- Testing implementation vs behavior
- Copy-pasted tests
- Overmocking (5+ = redesign)
- Magic values
- Test interdependence
- Commented-out tests

---

# Test Organization

Tests go next to code: same dir, separate file (`.test.rs`, `_test.go`).

## Why Separate Files

AI context cost changed calculus:
- Inline tests = noise in search/AI context (60% wasted tokens)
- Separate files = selective loading, clean prod files, faster grep

## Why Not Parallel Trees

Java's `src/main`/`src/test` was JVM workaround (no conditional compilation, classpath loading). Modern langs solved this.

### Rust
```rust
// mod.rs
#[cfg(test)]
#[path = "correlation.test.rs"]
mod correlation_tests;
```
Release builds exclude test module entirely.

### Go
`_test.go` files skipped by `go build`.

## Recommended Structure
```
src/
├── correlation.rs        # Prod
├── correlation.test.rs   # Tests
└── mod.rs
```

**Mutation testing benefit:** Survived mutation in prod file only; revert doesn't lose test improvements.

## Separation Exceptions
- Integration tests → `tests/`
- E2E → separate project
- Shared fixtures → `src/test_utils/`

Colocation is default.

## Test Location by Type

| Type | Tests | Location |
|------|-------|----------|
| Unit | Pure logic | Adjacent `.test` |
| BIT | Impl vs interface contract | Adjacent `.test` |
| Integration | Multi-component | `tests/` |
| E2E | Full system | Separate project |

## Tradeoffs

**Lost:** visibility, atomic commits, encouragement
**Gained:** clean prod files, efficient AI, faster search

Optimal org adapts to tooling. Constant: tests near code.

---

# Git Practices

## Branching
- Do not create branches/PRs unless explicitly asked
- When branching: short descriptive name
- Keep task changes in single branch
- Always branch off `main`/`master`; push back if asked to branch off anything else

## General
- Use `git log` for context/history
- Use `git rm` not `rm` for file removal

## Submodules
- Write-protected by default; explicit permission required for any submodule write (pointer, contents, `git submodule update`, sync, init, deinit)
- Prior approval does not carry forward — ask each time
- High blast radius; treat unexpected state as WIP

## Commits
- Keep messages brief, describe only code changes
- **NEVER mention Claude, Anthropic, AI, or "Generated with"**

## Breaking Changes
- Pre-1.0/new major: no backwards compat needed, clean up deprecated code
- Post-1.0 minor/patch: discuss first

## Hooks (lefthook)
- Config: `lefthook.yml`
- Pre-commit: lint, format, test (parallel)

## Pre-commit
- Run linters/formatters/tests
- Fix all issues before commit
- Auto-fix pre-commit errors without asking

## Bypass
- Only for WIP on feature branches
- Document with `--no-verify` + reason

---

# LSP for AI Coding Agents

LSP = semantic code nav. Reduces grep searches (30-60s) to ~50ms with 100% accuracy.

## Benefits

**Passive:** Auto error detection, self-corrects type errors/missing imports/undefined vars

**Active:**
- `goToDefinition` — symbol origins
- `findReferences` — all usages
- `hover` — types/docs
- `documentSymbol` — file functions
- `workspaceSymbol` — project-wide symbol search
- `goToImplementation` — concrete impls
- `incomingCalls`/`outgoingCalls` — call hierarchies

## Agent Guidance

Prefer LSP over Grep/Glob/Read:
- `goToDefinition`/`goToImplementation` for sources
- `findReferences` for usages
- `workspaceSymbol` for name lookups
- `hover` for types
- Text search only when LSP unavailable

## Troubleshooting

- **Disabled plugins:** Check `claude plugin list`—installed ≠ enabled
- **Missing binaries:** Verify PATH (`which pyright`, etc.)
- **Slow Java:** JVM warmup ~8s normal, fast after
- **Debug:** `~/.claude/debug/latest` for server logs

## Containers
Include language servers in image; use pre-configured overlays.

---

# 12-Factor App

Methodology for cloud-native SaaS: portable, scalable, maintainable.

## I. Codebase
1 repo, many deploys. Shared code → libraries via deps. Same codebase across dev/staging/prod.

## II. Dependencies
Declare explicitly (go.mod, package.json, requirements.txt). Isolate deps—no system-wide packages.

## III. Config
Store in env vars. Strict separation from code. Never commit secrets.

## IV. Backing Services
Treat as attached resources (DBs, queues, caches, APIs). Swap local↔cloud via config only.

## V. Build, Release, Run
- **Build**: code → executable (compile, fetch deps)
- **Release**: build + config, unique ID, immutable
- **Run**: execute in environment

Rollback = deploy previous release.

## VI. Processes
Stateless, share-nothing. Persist data in backing services. No sticky sessions—use Redis/Memcached for session state.

## VII. Port Binding
Self-contained; export HTTP via port binding. Routing layer maps hostname → port.

## VIII. Concurrency
Scale horizontally via process model. Web processes for HTTP, workers for background jobs. Use OS process manager.

## IX. Disposability
Fast startup, graceful shutdown on SIGTERM. Return incomplete jobs to queue. Design for sudden death.

## X. Dev/Prod Parity
Minimize time/personnel/tools gaps. Same backing services across environments. Use Docker for local parity.

## XI. Logs
Event streams to stdout. Env routes to aggregators (Splunk, Datadog, ELK). App decoupled from log management.

## XII. Admin Processes
Run one-off tasks (migrations, REPL, scripts) in identical environment. Admin code ships with app code.

---

# documentation

# Documentation Guidelines

## README Management
- Read README first
- Update when adding features
- Keep current

## Ask First Before Creating
- *.md files (REFACTORING.md, architecture docs)
- Planning/strategy docs
- Project tracking
- Meta-documentation

Exception: README updates permitted for new features

## Don't Document Progress in Code
- No "reorganized/refactored" comments
- No changelog-style inline docs
- Use commit messages instead

## Don't Include Change History in Files
- No "Updated on..." or "Previously..." annotations
- History stays in version control, not data

## Do
- Suggest documentation needs
- Summarize findings
- Update existing docs
- Create code, tests, config
- Write meaningful commit messages

---

# String Handling

## No String-Based Flow Control

Never use string approximations (startswith, endswith, contains, substring) for flow control—brittle, breaks silently.

```python
# Wrong
if error.message.startswith("Invalid"): ...
elif "not found" in error.message.lower(): ...

# Right
if isinstance(error, ValidationError): ...
elif error.code == ErrorCode.NOT_FOUND: ...
```

```go
// Wrong
if strings.Contains(err.Error(), "connection refused") { retry() }

// Right
if errors.Is(err, ErrConnectionRefused) { retry() }
```

## Error Messages as Constants

Define error messages as constants; reuse in tests. No magic strings.

```python
# constants.py
ERR_USER_NOT_FOUND = "user not found"

# service.py
raise NotFoundError(ERR_USER_NOT_FOUND)

# test_service.py
assert str(exc.value) == ERR_USER_NOT_FOUND
```

```go
var ErrUserNotFound = errors.New("user not found")
// service: return ErrUserNotFound
// test: assert.ErrorIs(t, err, ErrUserNotFound)
```

## Why

- String matching breaks silently on message changes
- Typos cause subtle bugs
- Constants enable IDE refactoring/find-refs
- Tests verify error handling, not string coincidence
- Enables i18n

---

# Decision Gating

Surface structural/interface decisions for sign-off before acting. The gate is a visible checkpoint — the user may approve or reject; what must not happen is the decision being made silently, buried in implementation.

## Gate these
- **Structural consolidation/splitting** — merging/splitting independent launchers, packages, modules; a shared abstraction across independent units. Changes *topology*, not just duplication.
- **Interface/seam changes** — public interface, trait, protocol, or extension seam. Ripples to every implementation; gate at least as hard as topology.
- **Operator-control trades** — trading the operator's control for author-side convenience (e.g. one polymorphic launcher vs discrete per-component launchers with full startup control).

## Don't gate
- DRY *within* a single unit (module, shared library).
- Local refactors that move no boundary and change no contract.

## Surface it
- **In plans:** a dedicated "Decisions for sign-off" section, each choice with 2–3 options. Never fold a topology/interface/control decision into a step bullet.
- **Mid-implementation:** a decision the approved plan didn't call out → stop and ask; don't pick and move on.
- **Default:** preserve existing separation and interface/seam contracts unless asked to change them. When unsure, treat it as a decision.

---

# Elegant Redo: Informed Reimplementation

Discard current impl and rebuild using lessons from first attempt.

## Philosophy
First impl teaches the problem. Second solves it well. Knowledge from edge cases, hidden requirements, and dead ends is the valuable output—not the code.

## Process
1. **Inventory knowledge** - Before deleting, document:
   - Hidden requirements, surprising edge cases
   - Architectural constraints revealed
   - Dependencies/interfaces to preserve
   - What worked vs what was over-engineered

2. **Identify elegant core** - With full problem knowledge:
   - Simplest abstraction covering all cases?
   - Natural data structures?
   - Where did first attempt fight the language/framework?
   - What's deletable vs essential?

3. **Scrap and rebuild** - Start clean:
   - No copy-paste from old impl
   - Let structure emerge from problem
   - Less code, fewer abstractions, simpler interfaces

4. **Validate** - New solution must:
   - Handle all discovered edge cases
   - Pass existing/improved tests
   - Be demonstrably simpler

## When to Apply
- Impl works but feels forced/overcomplicated
- Real problem shape doesn't match code
- Accumulated patches obscure design intent
- Developer explicitly requests fresh take

## Rules
- No sunk cost preservation—judge on current merit
- Simpler is better, but not at correctness cost
- Goal: minimum structure handling full problem naturally
- Keep tests (requirements); rewrite implementation

---

# Grill Me: Change Comprehension Gate

Quiz developer on their changes before PR creation to verify understanding.

## Process
1. Analyze diff (staged + unstaged)
2. Identify key decisions, trade-offs, edge cases, non-obvious implications
3. Ask pointed questions one at a time testing genuine understanding
4. Evaluate answers for real comprehension
5. Gate PR on sufficient understanding

## Question Categories
- **Intent**: What problem solved? Why this approach?
- **Impact**: What else affected? What breaks if this fails?
- **Edge cases**: Null/empty/concurrent behavior?
- **Trade-offs**: What sacrificed? Technical debt introduced?
- **Rollback**: How to revert safely? Blast radius?

## Grading
- **Pass**: Explains intent, impact, trade-offs clearly → proceed with PR
- **Partial**: Gaps exist → point out gaps, re-quiz weak areas
- **Fail**: Can't explain core decisions → no PR, suggest review areas

## Rules
- Rigorous but fair; test understanding, not memorization
- Scale difficulty to change complexity
- Explain wrong answers before continuing
- Never create PR until developer passes

---

# Prove It: Behavioural Diff Demonstration

Show concrete behaviour difference between main branch and current branch.

## Process
1. Identify behavioural claims - what should differ?
2. Design demonstrations exercising changed behaviour
3. Show before (main branch behaviour)
4. Show after (current branch behaviour)
5. Present side-by-side diff of inputs, outputs, effects

## Demonstration Methods
- Test output comparison across branches
- CLI invocation: same command, different output
- Code walkthrough: trace input through both paths, show divergence
- API calls: same request, different responses
- Error scenarios: differing failure modes

## Format

Per behavioural change:
```
## [Behaviour description]
### Main branch
Input: ... Output/Behaviour: ...
### This branch
Input: ... Output/Behaviour: ...
### What changed
[Concise diff explanation + why it matters]
```

## Rules
- Cover all intended changes, not just happy path
- Include edge cases and error conditions
- For pure refactors, demonstrate preserved behaviour
- Show evidence, don't just claim
- If local demo impossible, describe what would be tested and how

---

# just: Command Runner

Language-agnostic task runner. Define tasks (`just test`, `just lint`, `just build`) in a `justfile`.

## TOP: standard repo-root variable

Every justfile defines `TOP`:

```just
TOP := `git rev-parse --show-toplevel`
```

All paths relative to `TOP`. Non-negotiable. Hard-coded relative paths or `` break when invoked from subdirs or composed by parent justfiles.

```just
TOP := `git rev-parse --show-toplevel`

build:
    cargo build --manifest-path /Cargo.toml --release
```

## Local justfiles, composed at root

Place justfile next to code it manages. Compose via `mod`:

```just
# /justfile (root)
TOP := `git rev-parse --show-toplevel`

mod web   "/web/justfile"
mod api   "/api/justfile"
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
    rm -rf /target

[windows]
clean:
    Remove-Item -Recurse -Force \target
```

## Anti-Patterns

- Hard-coded relative paths (`./src/...`): break under composition.
- `` as `TOP` stand-in: scoped to local file, not composing parent.
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
      -v :/workspace \
      -v /justfile.container:/workspace/justfile:ro \
      -w /workspace build-env:latest just 

build: (_run "build")
test:  (_run "test")
```

```just
# /justfile.container
TOP := `git rev-parse --show-toplevel`

build:
    cargo build --manifest-path /Cargo.toml --release

test:
    cargo test --manifest-path /Cargo.toml
```

## Why it works

Load-bearing: `-v /justfile.container:/workspace/justfile:ro`. Bind mount obscures host `justfile` with `justfile.container` inside container. Inside: `just build` → container recipe (cargo). On host: `just build` → delegation spawning container.

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

# Container Overlay Pattern

Mount different build file over host's file inside container. Same command, different context.

## Problem
- Dual Makefiles: people forget which
- Conditional detection: cluttered `ifeq`/`else`
- Different commands: parallel names, duplicate docs

## Solution

```
project/
├── Makefile              # Host: delegates to container
└── Makefile.container    # Container: runs directly
```

**Host Makefile:**
```make
DOCKER_RUN := docker run --rm \
    -v ./:/workspace \
    -v ./Makefile.container:/workspace/Makefile:ro \
    -w /workspace myimage

build:
    $(DOCKER_RUN) make build
```

**Container Makefile:**
```make
build:
    cargo build
```

Key: `-v ./Makefile.container:/workspace/Makefile:ro` — file swap *is* detection.

## Separation
- **Host file:** image, volumes, network, env vars
- **Container file:** compilation, testing, build logic

## With just
```just
# justfile (host)
_run +ARGS:
    podman run -v ./justfile.container:/workspace/justfile:ro ... just {{ARGS}}
build:
    just _run build

# justfile.container
build:
    cargo build
```

## Edge Cases

**Devcontainer escape:**
```make
ifdef DEVCONTAINER
DOCKER_RUN :=
endif
```

**Podman/SELinux:** Add `:Z` to volumes.

## When to Use
- Polyglot projects, mixed teams, CI consistency
- Skip for simple projects or uniform environments

---

Dev Containers: `.devcontainer` config for consistent dev env w/ tools, deps, system reqs. Reproducible builds.

---

# testcontainers

Throwaway containers for tests—real deps vs mocks.

## Test Types

| Type | Tests | Location |
|------|-------|----------|
| Unit | Pure logic | Adjacent `.test` |
| BIT | Single impl vs interface | Adjacent `.test` (w/ testcontainers) |
| Integration | Multi-component | `tests/` |
| E2E | Full system | Separate project |

**BITs w/ testcontainers = unit-adjacent.** If cheap to test real, do it.

## Go Quick Start

```go
container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
    ContainerRequest: testcontainers.ContainerRequest{
        Image:        "postgres:16-alpine",
        ExposedPorts: []string{"5432/tcp"},
        Env:          map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "testdb"},
        WaitingFor:   wait.ForLog("database system is ready").WithOccurrence(2),
    },
    Started: true,
})
defer container.Terminate(ctx)
host, _ := container.Host(ctx)
port, _ := container.MappedPort(ctx, "5432")
```

## Wait Strategies

```go
wait.ForLog("Ready")
wait.ForHTTP("/health").WithPort("8080/tcp")
wait.ForListeningPort("5432/tcp")
wait.ForSQL("5432/tcp", "postgres", connStrFunc)
wait.ForAll(wait.ForLog("Started"), wait.ForHTTP("/ready"))
```

## Modules (Pre-configured)

```go
container, _ := postgres.Run(ctx, "postgres:16-alpine",
    postgres.WithDatabase("testdb"), postgres.WithUsername("user"), postgres.WithPassword("pass"))
connStr, _ := container.ConnectionString(ctx, "sslmode=disable")
```

Available: PostgreSQL, MySQL, Redis, MongoDB, Kafka, RabbitMQ, Elasticsearch, LocalStack, etc.

## Networks

```go
req := testcontainers.ContainerRequest{
    Networks:       []string{"test-network"},
    NetworkAliases: map[string][]string{"test-network": {"myservice"}},
}
```

## Files/Exec

```go
container.CopyFileToContainer(ctx, "local.conf", "/etc/app/config.conf", 0644)
exitCode, reader, _ := container.Exec(ctx, []string{"psql", "-U", "postgres", "-c", "SELECT 1"})
```

## Test Tags

```go
//go:build testcontainers
func TestPostgresStorage(t *testing.T) { /* ... */ }
```

## Rules

- Use modules when available
- Always `defer Terminate()`
- Proper wait strategies
- Never hardcode ports—use `MappedPort()`
- Tag images explicitly, prefer alpine
- Parallel OK—each gets own container
- Colocate BITs next to impl

---

# Go Style

## Format
- gofmt, Effective Go
- Funcs <50 lines

## Naming
- MixedCaps; exported=descriptive
- Interfaces: Reader/Writer/Closer
- Receivers: 1-2 char, consistent

## Docs
- Export IDs only; start w/ name; complete sentences

## Errors
- Return not panic
- Wrap: `fmt.Errorf("op: %w", err)`
- Check immediately; sentinel sparingly

---

# llm-tool-killer (ltk)

This project may run **ltk**, a pre-tool hook that inspects each shell
command before it executes and redirects it when a rule matches. Where
ctxloom shapes the context you see, ltk guides the commands you run.

## What it does

ltk parses the real command (resolving variables, unwrapping trivial
wrappers and sub-shells) and matches it against the project's rules in
`.ltk/config.yaml`. The first matching `deny` wins and returns a
`message`/`suggest` telling you what to run instead. Example:

    go test ./...   ->   blocked: "Run tests through the task runner."
                    ->   retry with `just test`

## How to work with it

- Treat a redirect as guidance, not a failure: read the suggestion and
  retry the command the way the rule asks.
- Prefer the project's task runner (e.g. `just <target>`) over invoking
  build/test/lint tools directly.
- **Agents do not cut releases.** ltk blocks `git tag` and release
  commands. Prepare the version bump and PR; a human (or CI) cuts the tag.

## What it is not

ltk is a cooperative redirect, not a sandbox. If explicitly instructed
to work around a rule the agent can, so it makes the easy, accidental
path the right one rather than enforcing hard isolation. For strict
"never" boundaries, run the agent in a container.

---

# taskloom

Persistent task tracking. Tasks live in a per-project append-only log
(~/.ctxloom/tasks/<project-id>.jsonl) and are keyed by harp IDs
(e.g. `swift-amber-falcon`). Statuses: `In Progress`, `To Do`,
`Deferred`, `Done`, `Archived`.

## MCP tools (served by `taskloom mcp`)

- `task_list({statuses?, term?, include_completed?, include_summary?})`
  — list/filter tasks. Set `include_summary: true` to also get
  per-status counts plus the in-progress harp IDs.
- `task_add({text, status?, trigger?})` — add a task with a fresh
  harp ID. Default status is `"To Do"`; `"Deferred"` requires a
  `trigger` (the condition that should revive it).
- `task_set_status({harp_id, status, trigger?})` — move a task
  between statuses.
- `task_edit({harp_id, text})` — replace a task's text in place.

Tasks are created and updated only through these tools (or the
`taskloom` CLI). The harp ID appears in `task_list` output so you can
reference a specific task in later calls.

## Plan stamping

When you edit a plan file (`CURRENT_PLAN.md`, `*-plan.md`,
`docs/*-plan.md`), the active session's harp name is auto-stamped
into the file's YAML frontmatter `sessions:` list. Plans and
sessions cross-reference without a separate database.
