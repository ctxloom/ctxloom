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
combination isolate, and — just as important — what does it NOT". Two
backends (kiro, antigravity) fail that second question today, by vendor
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
the matrix is measured against. Proven for all 5 backends by "workspace
`none` never touches any engine's config-home isolation at all" (Scenario
Outline, j002200_isolation.feature).

### workspace "worktree" x runtime "host" — the primary matrix

| backend | isolates | LEAKS | state |
|---|---|---|---|
| **claude-code** | config, credentials (`CLAUDE_CONFIG_DIR`, whole tree) | — | **ISOLATED** |
| **codex** | config, session state, credentials (`CODEX_HOME`, whole tree) | — | **ISOLATED** |
| **opencode** | config (`XDG_CONFIG_HOME`), credentials (`XDG_DATA_HOME`) — pinned at the Go level | — (contract-level: proven; exact payload: not independently re-proven by cucumber) | **ISOLATED** (partially NOT EXECUTED at the cucumber level — see note) |
| **kiro** | session state (`KIRO_HOME`) always; credential store (`XDG_DATA_HOME`) ONLY when `KIRO_API_KEY` authenticates a fresh one | credential store (global sqlite) when no `KIRO_API_KEY` | **LEAKS** without an API key, **ISOLATED** with one |
| **antigravity** | config, session state (curated `HOME`) | authentication (OS-session D-Bus keyring, ignores `$HOME`) AND file writes (`agy` ignores the launch cwd, always writes to a fixed global scratch) | **REFUSED** — ctxloom aborts the run (fatal `ClassIsolation` finding) rather than launch into either escape; `--degraded` downgrades to a working host run with neither isolated |

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
   `TestSeedCodexHome_*` (codex's container-auth mount reuses the same
   spec), the kiro `KIRO_API_KEY`-only container-auth tests, and
   antigravity's `resolveAntigravityContainerAuth`/
   `antigravityCredentialCopyMounts` credential-mount-or-degrade tests
   (fatal-amino, 2026-07-22 — no longer an always-`false` stub). Run via
   `just test`.

| backend | container auth mechanism | who can fix a gap | cucumber coverage |
|---|---|---|---|
| claude-code | `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN` env passthrough, else a read-write COPY of the host OAuth token mounted in | n/a — full lever | NOT EXECUTED (Go-pinned) |
| codex | `OPENAI_API_KEY` env passthrough, else a read-only mount of `~/.codex/auth.json` | n/a — full lever | NOT EXECUTED (Go-pinned) |
| opencode | `OPENROUTER_API_KEY` env passthrough, else a read-only mount of the seeded `auth.json` | n/a — full lever | NOT EXECUTED (Go-pinned) |
| kiro | `KIRO_API_KEY` env passthrough ONLY, no mount fallback | vendor-side (see kiro's leak entry below) | NOT EXECUTED (Go-pinned) |
| antigravity | seeds the host's file-based OAuth token (`~/.gemini/antigravity-cli/antigravity-oauth-token`) into the container, read-write, when present; else no-auth | **structural on host** (see antigravity's leak entry below); container now has a real, non-stub resolver (fatal-amino, 2026-07-22) | NOT EXECUTED — see the hand-measurement note below |

**Antigravity's container-axis fact is not a cucumber result at all** — it is
a hand measurement recorded directly in `internal/lm/isolation/auth.go`'s
`resolveAntigravityContainerAuth` doc comment (task `curated-scratch-home`,
2026-07-22, then corrected and implemented by task `fatal-amino` the same
week): running `agy` inside a plain docker container (no D-Bus session bus
reachable) DID trip its own file-based fallback ("Using file-based token
storage because no D-Bus session bus detected") — the detection+fallback
TRIGGER is confirmed real. The first attempt to prove the fallback
authenticates end-to-end mounted the WRONG file
(`~/.gemini/oauth_creds.json`) and failed ("You are not logged into
Antigravity"); that file turned out to be leftover state from the retired
standalone Gemini CLI (same shared `~/.gemini` directory), not an antigravity
credential at all. The CORRECT file, confirmed live, is
`~/.gemini/antigravity-cli/antigravity-oauth-token`, and
`resolveAntigravityContainerAuth` now seeds it (copy-then-mount-rw, mirroring
Claude Code's OAuth token treatment, since this token also carries a
refresh_token that self-renews) instead of always degrading. What remains
UNVERIFIED is narrower now: this resolver has not itself been driven through
a live containerized `agy` call with a real captured token (needs docker +
the real binary + a captured token together — out of band for this task; see
the "Antigravity container-axis full authentication success" entry under
UNKNOWN below). What is CONFIRMED: "runtime:container severs the
host-runtime leak's exact mechanism" (no `/run/user/<uid>/bus` inside a fresh
container namespace) — treated as settled, reproduced twice (the container
probe and the `env -i` reconciliation), both recorded in the same code
comment.

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

### LEAK 2 — antigravity: neither authentication nor file writes isolate on host runtime, so ctxloom refuses instead of warning through it

- **UPDATED 2026-07-22**: this used to be documented as a permanent, LOUD-but-
  non-fatal leak the run proceeded through by design. A second measurement
  the same day found antigravity's file writes ALSO escape a host worktree
  (below), which means neither of a worktree request's two possible payoffs
  holds for this engine on host — so ctxloom now REFUSES the run outright
  (a fatal `ClassIsolation` finding, downgradable via `--degraded`), the same
  severity as kiro's credential leak (LEAK 1 above), rather than warn and
  continue.
- **What escapes, concretely**: (1) the agy OAuth session — effectively,
  being logged in as the host user's own antigravity/Gemini account —
  reachable by every agy process on the host through the same channel,
  regardless of which curated `$HOME` ctxloom points a given agent at; AND
  (2) every file `agy` writes during a run, which land in ONE fixed global
  scratch directory (`~/.gemini/antigravity-cli/scratch/`) no matter which
  worktree/HOME the process was launched under — so two "isolated" agents
  share that one scratch tree exactly as they'd share credentials.
- **Why it escapes (the mechanism)**: agy authenticates through the
  OS-session D-Bus Secret Service keyring, addressed by a fixed, UID-derived
  socket path (`/run/user/<uid>/bus`) — not by any environment variable a
  caller controls. Measured directly: `env -i HOME=<fresh empty dir> PATH=...
  agy models`, with NO `DBUS_SESSION_BUS_ADDRESS`, NO `XDG_RUNTIME_DIR`, NO
  `DISPLAY`, and no other env at all, still authenticated via keyring — HOME
  relocation genuinely moves config and session state (a fresh `.gemini/`
  tree materializes wherever `$HOME` points; `chmod 000` on a fake HOME
  crashes agy, proving it reads the var) but has zero effect on which
  keyring socket agy reaches. Separately, MEASURED 2026-07-22 against agy
  1.1.5: `agy -p` ignores the launch working directory entirely — it never
  consults `$HOME`, the cwd, or any worktree path when deciding where to
  write, always landing in the same fixed global scratch path regardless of
  which "isolated" worktree launched it.
- **Who can fix it**: **STRUCTURAL** on host runtime, for both escapes —
  there is no environment-variable lever to redirect either one, so this is
  not "ctxloom forgot to wire something available" (opencode's shape) nor
  cleanly "vendor should add an env var" (though that WOULD also fix it —
  see the vendor-side note below). The fix that exists TODAY is changing the
  boundary itself: `runtime: container` severs the keyring socket at the
  kernel/namespace level (a fresh container has no `/run/user/<uid>/bus` at
  all) AND contains the global-scratch writes inside the container's own
  mount namespace — proven separately, and the reason ctxloom's refusal
  message points there instead of at any host-side workaround. A genuine
  VENDOR-SIDE fix also exists in principle — agy could ship an env var
  pointing at an alternate keyring collection and honor the launch cwd for
  its scratch writes — but it ships neither today, so on host runtime this
  is correctly marked structural, not vendor-fixable by any lever ctxloom
  could reach for.
- **What would go red if either escape closed**: `TestAcceptance/A_worktree_run_for_antigravity_refuses_to_start`
  (steps_j002200_isolation_matrix.go, via j002200_isolation.feature) asserts the fatal
  finding fires and names BOTH escapes. If agy ever shipped a real
  credential-scoping lever, `curatedHomeSpecs["antigravity"].authIsolated`
  (curatedhome.go) would need to flip to `true`; if agy ever started honoring
  the launch cwd for its scratch writes, `.workspaceViable` would need to
  flip to `true` too (either alone reverts the refusal to the old warn-only
  posture; both together would let it drop the finding entirely) — until
  then this scenario keeps failing loud (correctly conservative) if either
  measurement changes underneath it. The Go-level
  `TestWorktree_Antigravity_HostWorktreeRefused` /
  `...HostWorktreeRefusalDowngradesWithDegraded` /
  `...ContainerWrappedKeepsWarnOnlyNoRefusal` pin the identical contract,
  including the container exemption, at the unit level.

## What executed vs. what is defined-but-skipped, and why

- **EXECUTED, hermetically, every `just test-acceptance` run** (no live
  credential, no network call, no docker): all 5 backends x workspace
  {none, worktree} x runtime host — every row of the two tables above except
  the entire runtime:container column. 16 new scenario instances across 7
  Scenario Outlines/Scenarios in `j002200_isolation.feature`, confirmed green
  (203/203 total suite scenarios, this run).
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
  - The entire runtime:container column, all 5 backends — see the table's
    own note above (cost/speed tradeoff; Go-pinned instead).
- **Live vendor-drift detection** (does the REAL, currently-installed
  claude/codex/kiro/opencode/antigravity binary still honor these variables
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
- **`authCheckAntigravity` caveat**: none of this matrix's new scenarios rely
  on `live_engine_registry.go`'s `authCheckAntigravity` probe (they are
  hermetic). Flagging per the brief anyway: that probe is a LOCAL
  CREDENTIAL-FILE heuristic (`~/.gemini/antigravity-cli/antigravity-oauth-token`
  presence/expiry — corrected fatal-amino, 2026-07-22, off an earlier wrong
  `oauth_creds.json` guess), not a real login check — agy exposes no
  auth-status subcommand at all, and the probe's own doc comment records a
  measured case where a FRESH host with no such file still authenticated live
  via the keyring. Anyone relying on that probe elsewhere in the suite should
  treat "unavailable" as unreliable evidence of "not authenticated" on a HOST
  run, not proof of it — the keyring may still cover it there.

## UNKNOWN, explicitly

- **Antigravity container-axis full authentication success** (not just the
  fallback-trigger mechanism, and not just that the resolver now seeds the
  correct file — see the table's hand-measurement note above). Resolved by: a
  fresh, deliberately file-based (no reachable keyring) interactive `agy`
  OAuth login, then a containerized `ctxloom run --runtime container` against
  that credential. Not attempted here (needs real browser + user consent);
  tracked as a follow-up probe/CI run.
<!-- /doc:outro -->
