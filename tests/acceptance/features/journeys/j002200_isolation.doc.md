<!--
J002200 narration + outcome-matrix companion.

Unlike the other jN companions (pure prose, no assertions of their own), this
file also carries the rendered ISOLATION MATRIX the coordinator asked for —
backend x workspace x runtime, three states (isolated / LEAKS / not
executed), plus per-leak attribution (ctxloom-side / vendor-side / structural
/ uncertain) and the exact assertion that would go red if a leak closed. The
matrix is a DRIFT DETECTOR: it exists to go red the moment a vendor engine
(or a ctxloom regression) changes one of these measured facts, not to be a
green wall. Every claim below is either (a) proven by a cucumber scenario
named explicitly, (b) proven by a named Go unit test, or (c) marked
NOT EXECUTED / UNKNOWN with the reason and what would resolve it. Nothing
here is asserted from vendor documentation — see the codebase's own
"measure, don't cite docs" standard.

Marker convention: same doc:intro / doc:scenario / doc:outro pairs
j000200_setup.doc.md / j000400_multi_engine.doc.md / j002100_delegation.doc.md split on, for
the scenarios added by the isolation-matrix task. j002200's three PRE-EXISTING
scenarios (workspace/runtime axis distinctness, worktree cleanliness, the
generic container fail-loud contract) are not re-narrated here — they
predate this doc file and are already legible from the feature file itself.
-->

<!-- doc:intro -->
Two isolation axes exist in ctxloom — WORKSPACE (does the agent get its own
git worktree?) and RUNTIME (does its engine run on the host or in a
container?) — and they are independent: choosing one says nothing about the
other. This companion's job is to answer, for every backend ctxloom drives
and every axis combination, the question that actually matters to someone
trusting the boundary: not "does the run complete", but "what does this
combination isolate, and — just as important — what does it NOT". The whole
point of writing this down as an executable matrix, not a paragraph, is that
the moment a vendor opens one of those gaps, or ctxloom regresses one that is
currently closed, this matrix goes red and says which cell moved.

The matrix below is measured, not narrated: every cell traces to either a
named cucumber scenario in this file's feature (steps_j002200_isolation_matrix.go)
or a named Go unit test in internal/adapters/isolation. Where cucumber could not
independently prove a cell (the entire runtime:container column per engine),
that is stated as NOT EXECUTED with the
reason, never silently omitted.
<!-- /doc:intro -->

## The matrix

### Baseline: workspace "none" — no isolation requested, none happens (by design)

Every backend, both runtime axes: workspace "none" shares the live project
directory AND the engine's shared global config/credentials, unconditionally
— no config-home is provisioned, no finding fires, nothing is gated. This is
not a leak (nothing was asked to be isolated) — it is the control the rest of
the matrix is measured against. Proven for every backend by "workspace
`none` never touches any engine's config-home isolation at all" (Scenario
Outline, j002200_isolation.feature).

### workspace "worktree" x runtime "host" — the primary matrix

| backend | isolates | LEAKS | state |
|---|---|---|---|
| **claude-code** | config, credentials (`CLAUDE_CONFIG_DIR`, whole tree) | — | **ISOLATED** |

### workspace "worktree" x runtime "container" — NOT EXECUTED by this cucumber suite

Every cell in this column is **NOT EXECUTED** here. Building a real,
authenticated, per-engine container image to prove container-axis auth
resolution end to end costs minutes and a network pull per engine — the
opposite of what a fast drift detector needs, and the acceptance suite's own
throwaway project fixture has no devcontainer to build from anyway. Two
things stand in for it, deliberately, instead of a slow/flaky cucumber
scenario:

1. **The generic fail-loud/degrade CONTRACT** (any container that cannot
   actually launch — no daemon, no image, no container story — is a fatal
   finding that aborts the run unless `--degraded`) is already proven,
   engine-agnostically, by j002200's own pre-existing "Requesting a container with
   no runtime fails loud, or degrades under `--degraded`" scenario. Not
   restated here.
2. **Container credentials.** A container run authenticates exactly as a host
   run does: the agent's mode resolves to `engine.Credentials`, and the
   container environment mounts each shared store at its place under
   `$HOME`. That is pinned at the Go level (`TestCredentials_*` in
   `internal/adapters/isolation/sessionhome_test.go`), run via `just test`.

## Per-leak documentation

No backend ctxloom drives currently leaks on either axis — every row of the
tables above reads "—" in its LEAKS column. This section is where a leak gets
documented when one appears: what leaks concretely, the mechanism, who can fix
it, and what would go red if it closed.

## What executed vs. what is defined-but-skipped, and why

- **EXECUTED, hermetically, every `just test-acceptance` run** (no live
  credential, no network call, no docker): every backend x workspace
  {none, worktree} x runtime host — every row of the two tables above except
  the entire runtime:container column, confirmed green via
  `ACCEPTANCE_PATHS=features/journeys/j002200_isolation.feature`.
- **NOT EXECUTED by cucumber, pinned at the Go level instead**:
  - The entire runtime:container column, every backend — see the table's
    own note above (cost/speed tradeoff; Go-pinned instead).
- **Live vendor-drift detection** (does the REAL, currently-installed
  engine binary still honor these variables
  TODAY, not just what ctxloom's own code assumes) is NOT re-proven by this
  matrix's new scenarios — they are deliberately hermetic (a fake spy binary
  stands in for the real engine, by design, so this file never makes a live
  call). That axis already exists, separately, in this suite's `@live`
  infrastructure (`live_engine_registry.go`, exercised by J000200/J000400's
  `a real <engine> agent is available` scenarios) — the SAME
  `CLAUDE_CONFIG_DIR` wiring this matrix pins underlies those
  scenarios' credential-copy path already. A dedicated `@live`
  isolation-specific scenario (proving a real engine authenticates FROM its
  isolated config-home, never the host's) is future work, not fabricated
  here.

## UNKNOWN, explicitly

Nothing outstanding for this matrix.
<!-- /doc:outro -->
