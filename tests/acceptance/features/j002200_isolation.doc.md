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
combination isolate, and — just as important — what does it NOT". One
backend (kiro) fails that second question today, by vendor
design, not by a ctxloom bug ctxloom failed to fix — and the whole point of
writing this down as an executable matrix, not a paragraph, is that the
moment a vendor closes one of those gaps, or ctxloom regresses one that is
currently closed, this matrix goes red and says which cell moved.

The matrix below is measured, not narrated: every cell traces to either a
named cucumber scenario in this file's feature (steps_j002200_isolation_matrix.go)
or a named Go unit test in internal/lm/isolation. Where cucumber could not
independently prove a cell (opencode's exact spawned-env payload; the entire
runtime:container column per engine), that is stated as NOT EXECUTED with the
reason, never silently omitted.
<!-- /doc:intro -->

## The matrix

### Baseline: workspace "none" — no isolation requested, none happens (by design)

Every backend, both runtime axes: workspace "none" shares the live project
directory AND the engine's shared global config/credentials, unconditionally
— no config-home is provisioned, no finding fires, nothing is gated. This is
not a leak (nothing was asked to be isolated) — it is the control the rest of
the matrix is measured against. Proven for all 4 backends by "workspace
`none` never touches any engine's config-home isolation at all" (Scenario
Outline, j002200_isolation.feature).

### workspace "worktree" x runtime "host" — the primary matrix

| backend | isolates | LEAKS | state |
|---|---|---|---|
| **claude-code** | config, credentials (`CLAUDE_CONFIG_DIR`, whole tree) | — | **ISOLATED** |
| **codex** | config, session state, credentials (`CODEX_HOME`, whole tree) | — | **ISOLATED** |
| **opencode** | config (`XDG_CONFIG_HOME`), credentials (`XDG_DATA_HOME`) — pinned at the Go level | — (contract-level: proven; exact payload: not independently re-proven by cucumber) | **ISOLATED** (partially NOT EXECUTED at the cucumber level — see note) |
| **kiro** | session state (`KIRO_HOME`) always; credential store (`XDG_DATA_HOME`) ONLY when `KIRO_API_KEY` authenticates a fresh one | credential store (global sqlite) when no `KIRO_API_KEY` | **LEAKS** without an API key, **ISOLATED** with one |

### workspace "worktree" x runtime "container" — NOT EXECUTED by this cucumber suite

Every cell in this column is **NOT EXECUTED** here. Building a real,
authenticated, per-engine container image to prove container-axis auth
resolution end to end costs minutes and a network pull per engine — the
opposite of what a fast drift detector needs, and the acceptance suite's own
throwaway project fixture has no devcontainer to build from anyway. Two
things stand in for it, deliberately, instead of a slow/flaky cucumber
scenario:

1. **The generic fail-loud/degrade CONTRACT** (any container that cannot
   actually launch — no daemon, no image, unresolvable auth — is a fatal
   finding that aborts the run unless `--degraded`) is already proven,
   engine-agnostically, by j002200's own pre-existing "Requesting a container with
   no runtime fails loud, or degrades under `--degraded`" scenario. Not
   restated here.
2. **Per-engine container auth RESOLUTION** (does THIS engine's specific
   auth plan — env passthrough, credential mount, or "no lever at all" —
   resolve the way the matrix below claims) is pinned at the Go level,
   thoroughly, in `internal/lm/isolation/auth_test.go` and
   `curatedhome_test.go`: `TestResolveClaudeContainerAuth_*`,
   `TestPrepareCodexHome_*` (codex's container-auth mount reuses the same
   spec), and the kiro `KIRO_API_KEY`-only container-auth tests. Run via
   `just test`.

| backend | container auth mechanism | who can fix a gap | cucumber coverage |
|---|---|---|---|
| claude-code | `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN` env passthrough, else a read-write COPY of the host OAuth token mounted in | n/a — full lever | NOT EXECUTED (Go-pinned) |
| codex | `OPENAI_API_KEY` env passthrough, else a read-only mount of `~/.codex/auth.json` | n/a — full lever | NOT EXECUTED (Go-pinned) |
| opencode | `OPENROUTER_API_KEY` env passthrough, else a read-only mount of the seeded `auth.json` | n/a — full lever | NOT EXECUTED (Go-pinned) |
| kiro | `KIRO_API_KEY` env passthrough ONLY, no mount fallback | vendor-side (see kiro's leak entry below) | NOT EXECUTED (Go-pinned) |

## Per-leak documentation

### LEAK 1 — kiro: subscription-authenticated credential store is never isolated

- **What leaks, concretely**: kiro-cli's subscription OAuth token (GitHub/
  Google/Builder-ID social login) and its `conversations_v2` session store,
  both mixed into ONE sqlite database at
  `$XDG_DATA_HOME/kiro-cli/data.sqlite3` (default
  `~/.local/share/kiro-cli/data.sqlite3`). Every kiro-cli invocation on the
  host — the isolated agent and every other agent/session — reads and writes
  the SAME file.
- **The store is written, not just read — even by a "status" probe**:
  measured 2026-07-22 (reproduced independently), a bare `kiro-cli whoami` —
  used elsewhere in this codebase as a read-only auth check, no login/logout
  involved — advances `data.sqlite3`'s mtime with its size unchanged. Two
  "isolated" kiro agents calling `whoami` against the shared host store are
  not just reading a common identity, they are mutating a common file. See
  `tests/acceptance/live_engine_registry.go`'s `authCheckKiro` for the
  caveat this puts on treating that probe as side-effect-free.
- **Why it leaks (the mechanism)**: kiro-cli resolves its credential store
  from `$XDG_DATA_HOME`, a variable ctxloom's `KIRO_HOME` isolation lever
  does NOT touch (`KIRO_HOME` relocates session state only). ctxloom COULD
  point `$XDG_DATA_HOME` at a per-agent directory too — and does, but ONLY
  when `KIRO_API_KEY` is present, because a fresh, empty, per-agent
  `XDG_DATA_HOME` authenticates headlessly via that key (live-verified
  against kiro-cli 2.12.1) with no browser needed. Without the key, isolating
  the var would silently strand the agent logged out — worse than not
  isolating it — so ctxloom refuses instead (`gateHomeVars`,
  `internal/lm/isolation/worktree.go`), and that refusal is itself the
  positive proof: the fatal finding text names exactly what would not
  relocate ("isolating XDG_DATA_HOME would relocate kiro's credential
  store... sharing the host's global store instead").
- **Who can fix it**: **VENDOR-SIDE**, with a ctxloom-side nuance. kiro-cli
  offers exactly one lever that authenticates a per-agent credential store
  without a browser (`KIRO_API_KEY`) — ctxloom already uses it, fully, the
  moment it is present (the "isolates its credential store too" scenario
  below proves this). For the SUBSCRIPTION-login case, no ctxloom-side
  redirect exists to reach for: `KIRO_HOME` is the only isolation var
  kiro-cli exposes, and it structurally does not cover credentials. This is
  not a case of an available lever sitting unused (the opencode shape,
  before sunny-saga) — it is a lever kiro-cli does not offer for this one
  auth path. If kiro-cli ever honors `KIRO_HOME` (or a new var) for
  credentials too, that is the fix; ctxloom's job then is only to widen
  `HonoursVarForCreds` to match, not to invent a mechanism from nothing.
- **What would go red if the leak closed**: `TestAcceptance/A_worktree_run_for_kiro_isolates_only_session_state_without_an_API_key...`
  (steps_j002200_isolation_matrix.go, via j002200_isolation.feature) asserts the
  fatal finding fires and names the credential-store mechanism, with no
  `KIRO_API_KEY` set. If kiro-cli started honoring `KIRO_HOME` for
  credentials, ctxloom's OWN registry (`credentialSeedSpecs["kiro"]` in
  auth.go) would need updating to `HonoursVarForCreds: true` — until that
  ctxloom-side change lands, this scenario keeps failing loud (correctly
  conservative), and the Go-level `TestWorktree_KiroFailLoudWithoutApiKey` /
  `TestWorktree_KiroIsolatesXDGWithApiKey` pin the same contract at the unit
  level.

## What executed vs. what is defined-but-skipped, and why

- **EXECUTED, hermetically, every `just test-acceptance` run** (no live
  credential, no network call, no docker): all 4 backends x workspace
  {none, worktree} x runtime host — every row of the two tables above except
  the entire runtime:container column, confirmed green via
  `ACCEPTANCE_PATHS=features/j002200_isolation.feature`.
- **NOT EXECUTED by cucumber, pinned at the Go level instead**:
  - opencode's exact spawned-env payload (the `XDG_DATA_HOME` vs.
    `XDG_DATA_HOME/opencode` nesting subtlety) — opencode's real launch path
    is ACP (a stateful JSON-RPC handshake over stdio), not a plain oneshot
    exec; the spy fixture every other backend uses never completes that
    handshake, so no output reaches it (confirmed by hand: the file this
    suite's spy would write is never created). opencode's fail-loud/warn
    CONTRACT (the "refuses to start" / "proceeds once its API key rides the
    environment" scenarios) IS still proven for it — those fire before any
    engine spawn is attempted. The exact payload is pinned instead by
    `internal/lm/isolation/auth_test.go`'s
    `TestHostCredentialSeed_OpencodeSeedsAuthJsonUnderXdgDataOpencode`.
  - The entire runtime:container column, all 4 backends — see the table's
    own note above (cost/speed tradeoff; Go-pinned instead).
- **Live vendor-drift detection** (does the REAL, currently-installed
  claude/codex/kiro/opencode binary still honor these variables
  TODAY, not just what ctxloom's own code assumes) is NOT re-proven by this
  matrix's new scenarios — they are deliberately hermetic (a fake spy binary
  stands in for the real engine, by design, so this file never makes a live
  call). That axis already exists, separately, in this suite's `@live`
  infrastructure (`live_engine_registry.go`, exercised by J000200/J000400's
  `a real <engine> agent is available` scenarios) — the SAME
  `CLAUDE_CONFIG_DIR`/`CODEX_HOME` wiring this matrix pins underlies those
  scenarios' credential-copy path already. A dedicated `@live`
  isolation-specific scenario (proving a real engine authenticates FROM its
  isolated config-home, never the host's) is future work, not fabricated
  here.

## UNKNOWN, explicitly

Nothing outstanding for this matrix as of the antigravity engine's removal —
the one entry that lived here (antigravity's container-axis authentication
success) was retired along with the engine itself.
<!-- /doc:outro -->
