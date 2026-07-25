# ctxloom — Architectural Review Findings

**Base:** `0f59fbae2399c35316cce04cb952e1a83599c6cf` (branch `release/0.7`) · **Review branch:** `docs/architecture-review`
**Scope:** 164,271 reviewable LOC · 694 files · 123 packages · 164 units (162 reviewed, 2 skipped as generated)
**Method:** phase 2 — one Opus reviewer per unit, every line read. phase 3 — 8 lens synthesizers over the 6 MB unit corpus. phase 4 — coordinator ranking + verification.
**Artifacts:** per-unit `~/.ctxloom/sessions/bent-mixed-able/arch-review/units/U*.md` · lenses `…/synthesis/0*.md` · raw threads `…/CROSSCUTTING.md` · this file is the register.

---

## How to read this

Every finding carries a verification provenance. **This is the most important column** — the review's own headline claims moved in both directions under scrutiny.

| tag | meaning |
|---|---|
| **VERIFIED** | the coordinator re-read the source and confirmed it, independent of the agent that reported it |
| **REPRODUCED** | an agent executed it against a real binary and it behaved as described |
| **CONFIRMED** | a synthesizer verified it against source and stated its method |
| **CLAIMED** | asserted by a unit review, not re-verified — **do not act on these without checking** |

### This file is the action list, NOT a census — read both

**Corrected 2026-07-25.** An earlier version of this section said "381 HIGH across 162 units" and let that stand in for the corpus. **That was wrong and it undersold the corpus by a factor of six.** The real totals, mechanically extracted:

| | count |
|---|---|
| findings in the corpus | **2,268** |
| HIGH | 376 |
| MED | 999 |
| LOW | 871 |
| ranked items in THIS file | **20** |

That is 113:1 compression, and **no finding in the corpus is cited by ID anywhere in this file** — so there is no path from this register back to the evidence behind it.

**The complete census is [`docs/architecture/findings-index.md`](docs/architecture/findings-index.md)** — all 2,268 findings, mechanically extracted by script (no model judgement), sorted by severity, each with unit ID, `file:line`, category, and claim. It exists because this file answers *"what do we fix first"* and cannot answer *"what is the actual state of this codebase."* Those are different questions and they need different artifacts.

**How to use them together:**
- **Act** from this register: it is ranked, deduped, and every entry carries a verification provenance.
- **Look up** in the index: it is lossless and greppable, and it is where the ~100 distinct HIGH findings that did not survive ranking still live.
- A row in the index is **one reviewer's claim about one unit**. It is NOT verified unless this register says so. Treat every index row as CLAIMED until checked — this review downgraded or refuted seven headline claims under exactly that scrutiny.

Applying the observed dedup ratio (~3:1, measured by the failure-semantics lens on its own slice) to 376 HIGH suggests roughly **125 distinct HIGH findings**. This register names 20. **The remainder are real, evidence-backed, and out of scope for the remediation plan** — they are backlog, not noise.

---

## Read this before acting: what changed under verification

Seven of the review's biggest claims moved. This is the argument for the register rather than the raw corpus.

| claim as filed | verdict after verification |
|---|---|
| X10 "THE CENTRAL FINDING" — hand-mirrored converters, one root cause | **Two** root causes needing **two different fixes**. Warranted on evidence (11 confirmed drops, 0 refutations), **overstated on consequence** |
| `internal/operations` returns nil-error-with-failure-in-result → one structural fix | **REFUTED.** 12 of 16 result types *are* read by their caller. **Five** independent root causes |
| X27 — tests bypass the serialization boundary | **Mechanism replaced.** Both converters are at 100% statement coverage. The round-trip test asserts one *named field*, not struct equality |
| R3 — ltk has no unanalyzed state, fails open | Downgraded by the coordinator, then **re-confirmed against the coordinator**. See X-D2. **S1 stands** |
| U041 — container leaked on error path | **Qualified.** Not leaked today; the documented backstop is false and a real publication window exists |
| X61 — zip-slip escalation | **REFUTED.** `topDir` is sanitized; data-loss only |
| phase-2 downgrade of the codex credential symlink | **REVERSED** with a reproduction — gitignore does not apply to tracked files |
| 2026-07-24 retry storm "fixed" | **REFUTED.** Still live — see T5 |

**Process rule this produced, worth keeping:** a `CONFIRMED` label is only as good as how far the verification walked. R3 was confirmed by a structural read that stopped one frame short of a config knob; the coordinator's downgrade then stopped one layer short at a *doc comment* rather than a *function signature*. `ExpandWrappers(…) (truncated bool)` settled in one line what two prose readings got wrong. **Prefer signatures and call topology over comments when adjudicating.**

---

## FIX ORDERING — read before scheduling any work

Five constraints. At least two fixes are unsafe applied alone.

1. **ACP path confinement MUST land before the `ChatRequest.Runtime` wire fix.** Repairing the wire drop *activates* the path-confinement hole it currently masks (S3 + X31). Fixing the "obvious" bug first opens a vulnerability.
2. **Do not wire `extra_args` into argv before its allowlist exists** (S14). Today it is a documented "enforcement point" with zero readers — harmless. Wiring it without the allowlist makes it an injection point.
3. **S5's two halves must both land**; neither closes the other, and either alone reads as fixed.
4. **Do not add `--ignored` to `IsDirty` alone.**
5. **T1's guard (b) should land before or with (a).** Refusing to write an empty lockfile over a non-empty one closes the whole class; fixing only the fallback closes one trigger.

---

## Tier 1 — act on these

### T1. `remote upgrade` silently wipes `lock.yaml` and reports success — **VERIFIED**
Highest data-loss finding in the review. Every link re-read by the coordinator.

`remote_upgrade.go:35` uses `loadConfigOrFallback`, which on ANY config-load error returns a minimal empty config (`startup_helpers.go:38-39`). Empty config → no profile definitions → `closureRoots` empty (`depgraph.go:95-107`) → `proposed` and `unexpanded` both empty → the carry-forward guard at `upgrade.go:77` **never fires** → `Save(newActive)` at `:90` is **unconditional** and writes an empty lockfile, erasing every pin → `advanced == 0` → prints **"Everything is up to date."**, exit 0.

- **Root cause, one line:** a fault-tolerant fallback intended for read-only commands (its own comment names `remote update` and `search`) is used by a destructive one.
- **The author guarded the wrong case.** `upgrade.go:28-30` reasons explicitly about `Save(newActive)` erasing entries — and applies that reasoning to root-set narrowness and `unexpanded`, not to the empty closure.
- **Security-relevant, not just inconvenient:** per S12 retraction state is cleared by `remote upgrade`, so a wipe silently **un-retracts** previously retracted content.
- **Trigger is routine:** a config-load error, not an exotic state.
- **Fix:** (a) `remote upgrade` must refuse a fallback config and fail loud; (b) `Save` must refuse to write an empty lockfile over a non-empty one absent an explicit force. **Do (b) regardless** — it closes the class.

### T2. `deny_tools` and `skills` are dropped on the LAUNCH wire — **VERIFIED**
Reported independently by three lenses (D1, W2, S7); reach settled by the coordinator.

> **Scope correction (coordinator, phase 4).** An earlier statement of this finding — including mine — said these fields "never reach any backend, on any path." **That over-claimed.** They DO reach engines by other routes: `DenyTools` via `internal/operations/profile_materialize.go:115` and `internal/operations/hooks.go:452` (both `AssembleManagedDenyTools`, writing settings files directly), and `Skills` via each engine's own `surfaces.go` (kiro `:237`, opencode `:136`, antigravity `:231`). **What is broken is the LAUNCH path specifically.** State it that way; the narrower claim is the true one and is still serious.

`agent.ManagedConfig` (`shared/agent/backend.go:348-363`) declares 7 fields including `Skills` and `DenyTools`. The proto `ManagedConfig` (`lm/grpc/llm.proto:425`) declares 5. `ManagedConfigToProto` (`lm/grpc/managed.go:22-28`) and `managedConfigFromProto` (`:37-43`) drop both, in both directions. The consumer (`shared/agent/launch_backend.go:253-257`) reads them and always receives nil.

- **Reach — this is what the agents disagreed on.** Exactly ONE site constructs `SetupRequest.Managed` (`lm/grpc/server.go:204`) and exactly TWO producers feed it: `cli/run.go:984` (`ctxloom run`) and `operations/oneshot.go:405` (oneshot/delegation). **No in-process path bypasses the converter.**
- `DenyTools`' own comment calls it "the deny-tools.md **root-cause fix**" — a hole deliberately closed, inert since.
- **The doc comment is part of the defect:** `managed.go:11-14` tells any auditor the messages mirror "field-for-field, so these converters are mechanical."
- **Fix:** add both fields to the proto; then T7's parity test to prevent recurrence.

### T3. ltk deny rules bypassable — **REPRODUCED** (3 ways, against the installed binary)
Bypass via a trailing token, an uninstalled interpreter, or one wrapper layer (S1).

Mechanism, coordinator-confirmed: the `Defaults.OnParseError` policy applies **only** at the top-level parse (`ltk/app/app.go:181-187`). `ExpandWrappers` is declared `(truncated bool)` (`ltk/frontend/wrap.go:59`) and **structurally cannot report a nested parse failure**; `wrap.go:80` discards that error, so a wrapped command whose inner text fails to parse is never evaluated and **no policy applies**.

- Note what is *correct* nearby, so the fix does not regress it: `app.go:194` fails CLOSED on truncated expansion, and `wrap.go:74-79` deliberately salvages partial parses.
- **Two further allow-paths in the same subsystem** — CLAIMED (phase-4 documentation agent, cited to source; not coordinator-verified): `cmd.Frontend.Parse` never returns an error at all, and a **missing frontend degrades to allow** while `Registry.Supports` has **zero call sites**. If these hold, the unanalyzable-input surface is wider than the nested-parse path alone, and a fix addressing only `wrap.go:80` would leave it open. **Verify both before scoping the fix.**
- **Fix:** give the nested path a way to report unanalyzability and route it through the same policy — then audit every other route by which an unparsed or unsupported input reaches an allow. The `ExpandWrappers` signature is the blocker for the first; `Registry.Supports` having no callers suggests the second was intended and never wired.
- Context: ltk is a cooperative redirect, not a sandbox. This bounds the severity — it does not excuse a guard that can be stepped around.

### T4. Manifest-less skill trust preimage is a constant, and verification is skipped on the same predicate — **REPRODUCED**
`{"preimage":"ctxloom-exec/1","manifest":[]}`, sha `502727b7…` (executed), with `loader_skills.go:105` skipping verification on the identical condition (S2). Two independent failures of the same trust check, in one branch. Directly undermines the signed-context claim.

### T5. The 2026-07-24 retry storm is **NOT** fixed — **CONFIRMED**
The agent overturned its own first-pass conclusion. The gate bounds consecutive launch *failures*, but `noteLaunchAttached` **resets the counter on every attach** — so an attach-then-die child relaunches forever at `launchBackoff(0)` = **zero delay** (C12). Compounded by C13: plane-2 `agent_stop` never calls `cancelLaunch` (1 caller repo-wide), which is why "the loop outlived every stop issued against it."

This is the architecture behind the observed 846 zombies / 208 orphaned runners. Those specific symptoms are durably fixed; **the generating mechanism is not.**

### T6. Six of sixteen CI gates do not gate — **VERIFIED** (by exit code; 3 independently re-checked)
| gate | result |
|---|---|
| `complexity-check` | **exit 1** — red and ignored |
| `test-conformance` | **exit 1** *and in no workflow* — it caught a real claude-vs-codex divergence nobody is watching |
| `gen-schemas` (untagged) | **exit 0 having emitted zero bytes** |
| `validate` | **exit 0 validating 0 files** (`git ls-files .ctxloom` → 0; `.ctxloom/*` gitignored) |
| `gen-schemas-check`, `extract-defaults -check` | **do not exist** — despite comments citing them as protection |
| gofmt | **no gate anywhere**; 19 dirty files |

`gen-schemas` is this project's characteristic bug living inside its own CI. Enforcing and real: `test`, `gen-mcp-schemas-check`, `gen-docs-check`, `vet-integration`, and `CTXLOOM_REQUIRE_DOCKER=1` at `ci.yml:236`.
**Unknown:** `just lint` returned exit 3 on a host toolchain mismatch — genuinely undetermined, re-run in the devcontainer.

**The complexity gate also has a structural blind spot, independent of being red** — CLAIMED (phase-4 documentation agent): **lizard does not descend into func literals**, so the ~930-line `runCmd.RunE` closure (`cli/run.go:367-1300`) — the single largest body of logic in the CLI — is invisible to it even when the gate runs. Two separate problems: the gate does not run, *and* it could not see the worst offender if it did. Fixing only the red does not close this.

### T7. The one testing change with the highest yield — **CONFIRMED**
A dropped field is an **absent statement**, and no coverage, mutation, or complexity metric can point at a line nobody wrote. The existing round-trip test asserts `req.MCPServers == back.MCPServers` — one *named field*, not `req == back`. MCPServer survives because it is the field the assertion names.

**One reflective total-struct round-trip helper (~1 day) across every hand-mirrored converter covers ~104 HIGH wire drops.** This is the highest findings-per-unit-of-work item in the review. Runner-up (~1 hour): invert the 6 tests that currently *pin* silent no-ops as intended behavior.

---

## Tier 2 — structural, schedule deliberately

- **T8. Nine MCP arguments are declared, accepted, and have zero effect** — CONFIRMED (W1). All 8 agentcoord ones are published as real in the *generated public* reference. Four artifacts agree (proto → generator → runner → docs); only the handler disagrees. **Fix = one generator assertion:** every projected input field must be read by its handler.
- **T9. The exit-0-on-failure family: five independent root causes**, not one and not thirty (R1–R5). R1 no exit-code policy for management commands (`strictness` is deliberately launch-only: 57 producers, 7 drains, all launch-path). R2 failure not representable in the return type. R3 see T3. R4 "absent" vs "unreadable" conflated. R5 empty input parses as valid — underlies the whole zero-payload family.
- **T10. Trust store designed fail-closed, implemented fail-open** (F6) — CONFIRMED.
- **T11. Six real import cycles deferred into external `_test` packages** — VERIFIED (L1): `coord↔cli/tui`, `termui↔cli/tui`, `transcript↔lm/grpc`, `shared/agent↔{claude,codex,kiro}`. **Four were invisible to every unit review** — each is only visible from outside a single unit. Zero production cycles.
- **T12. Engine identity enumerated in four rosters with four different memberships** — CONFIRMED (L3). `internal/operations` importing `claude`/`codex`/`kiro` is a literal ADR-0026 violation in the core.
- **T13. `internal/acp` fs handlers serve any absolute host path** — CONFIRMED (S3). Currently masked by the `ChatStart.runtime` drop. **See fix-ordering constraint 1.**
- **T14. Container credential fail-open** — CONFIRMED (S4): `containerProfileFor`'s default hands claude credentials to any unrecognized engine; the registered `acp` backend reaches it.
- **T15. Codex credentials copied into the repo tree, unignored, write follows a tracked symlink** — CONFIRMED + REPRODUCED (S5); phase-2's downgrade reversed.
- **T16. Runner cell-path boundary anchored to the coordinator's cwd** — CONFIRMED (S6), U038 upheld: `agent_report.publish_paths` / `agent_fetch_artifact.dest_path` can write into the parent tree under `workspace:worktree`.
- **T17. `ctxloom init` overwrites `.ctxloom/config.yaml` and `remotes.yaml`** (`operations/init.go:76,84`, direct `afero.WriteFile`, no merge) while `default.yaml` at `:104-105` is write-if-absent — inconsistent preservation inside one function. Init is documented as re-runnable. Tracked as taskloom `gray-wick`.
- **T18. No depguard or architecture linter exists** — CONFIRMED (L13). Of four nameable rules, **three are within a few edits of passing today**. Cheapest durable win in the layering register.
- **T19. `--format` is declared far more widely than it is honored** — CLAIMED (phase-4 documentation agent, tabulated against `format_coverage_test.go`): **~30 commands accept `--format` and never call `emit()`**, including **all nine `remote` commands and all four `config` commands**. The coverage registry itself carries three inaccuracies. This is T8's disease on the CLI surface rather than the MCP surface — an argument accepted, validated, and discarded, where the caller cannot learn it did nothing. A machine-readable contract exists (`format_coverage_test.go`), so the fix is to make it authoritative and enforcing rather than descriptive.

---

### T20. STANDARD — cobra `RunE`/`Run` bodies must be named functions, never inline closures
**Adopted as a project standard 2026-07-25.** Measured, not asserted: **94 inline `RunE: func(` closures** vs **74 already-named references** across `internal/` + `cmd/`. The convention already exists and is applied unevenly — this makes it the rule.

**Why this is a correctness standard and not a style preference:**

1. **The complexity gate structurally cannot see a func literal.** lizard does not descend into them, so `runCmd.RunE` — a ~930-line closure at `cli/run.go:367-1300`, the largest body of logic in the CLI — is invisible to CI even when the gate runs. Every inline `RunE` is a hole in the CCN gate exactly proportional to its size. This is the mechanism behind T6's blind spot; the standard closes it at the source rather than by tuning the linter.
2. **A named function is directly testable.** An inline closure can only be exercised through cobra's dispatch, which is why `run_owned.go`'s effectful functions sit at 0% coverage while a genuine data race lives in them (T5/C3).
3. **Stack traces and profiles name it.** `func1` tells a reader nothing.

**Rule:** `RunE: runFoo` where `func runFoo(cmd *cobra.Command, args []string) error` is a package-level function. The cobra command literal declares wiring; it holds no logic.

**Enforcement:** this is the first concrete rule for the missing architecture linter (T18) and needs no AST work to start — `RunE: func(` is a grep-able violation, so a CI gate is a few lines. Add the gate and the rule together; a standard without a gate regresses, and this codebase has 6 gates that already do not gate (T6).

**Migration:** mechanical and low-risk — extraction with no behavior change, done per-command. Sequence it largest-first (`run.go` alone recovers gate visibility over ~930 lines). **Expect the extraction to surface findings**, because the newly-visible functions immediately face the CCN gate; budget for that rather than being surprised by it.

**Codify it beyond this repo:** the rule belongs in the Go development bundle's guidance so it reaches new code, not only this cleanup.

---

## Deletion register — ~1,675 production LOC

CONFIRMED by hand-classifying every `rg` hit as declaration / doc / test / real use.

| tier | LOC | notes |
|---|---|---|
| safe | ~890 | `ContentCommands` ~165 (6 implementations, **0 invocations**); `internal/claude/agentfiles.go` whole file 162; lockfile 113; operations 150; launch-settlement 120; `Chroot` 50 |
| needs interface change | ~345 | incl. 5 `Kind()` methods + `SurfaceSet.Deliveries()` — implemented across 5 backends purely to satisfy an interface used only by tests |
| breaks a public contract | ~440 | decide deliberately |

Plus 70 orphaned generated schema files (~284 KB) embedded in every binary.

**Two starting claims were refuted** — the reason this register is trustworthy: `pkg/clifmt` is heavily used (20+ files), so it is a **move to `internal/`, 0 LOC removed**, not a deletion; and `resources/schema/input/` is live (feeds `gen-docs`) — only the 70 gitignored `gen/` files are orphaned.

`SetExecutablePathForTesting` is called from production (`operations/hooks.go:65`) — a defect needing a decision, **not** a deletion.

**Method note:** `deadcode` is reachability-from-`main`. It cannot see unused exported surface, reflection/interface dispatch, or reachable-but-pointless code. Absence from its baseline is **not** evidence of liveness.

---

## Counter-evidence — recorded so the register is not read as worse than it is

- **Fail-open is NOT house style.** X17/X21 upheld and extended: isolation defaults and enums fail closed; exactly **two** proto3 enum inversions exist, both local. Say "two local defects", not "fails open". A subsystem-by-subsystem polarity table is in `synthesis/03`.
- **Zero production import cycles.** The six in T11 are the complete set, all test-deferred. Eight units independently report clean dependency direction; the lower graph is well-ordered.
- **`mcpschema` is the counter-example to copy** (X30) — machine-enforced against the runner surface at startup and in CI. It is the template every other surface should adopt.
- **`decodeConfigPatch`** (`cli/util_config_write.go:236-248`) is the best anti-silent-no-op guard in the codebase — promote it repo-wide.
- **`internal/shared` is justified** for 26 of its 32 members; the frontend is a true sink; `lm/backends` is a genuine polymorphic edge.
- **Concurrency, honestly:** of 7 live `ctxloom run` processes on the review machine, **6 have no architectural explanation** — they are foreground jobs of live shells. One is C1. No ctxloom containers are leaked.

---

## Open — explicitly not established

Do not treat these as findings.

- `just lint` verdict on this commit — **unknown** (exit 3, host toolchain mismatch). Re-run in the devcontainer.
- Not executed at all: `test-acceptance`, `test-integration`, the mutation gate.
- Agent 08 §F lists ~13 high-severity + 6 structural-cluster items left **CLAIMED**.
- Left CLAIMED elsewhere: D10/D13/D14, parts of D11/D12; transcript payload mirrors and `Record.Engine`; MCP resources; a systematic output-schema sweep; `internal/acp` L0 `$defs`.
- S11's `ssh-keygen` interop half; X68 timings; 2 of 4 retraction paths; `VerifyPublisher` nil-root reachability.
- No container, codex, or delegation run was executed by any synthesizer.

---

## Markup instructions

Per the standing decision, **no taskloom tasks were filed from this review.** Mark this file up — strike what you reject, annotate what you want — and tasks get seeded from your markup, not from the register wholesale.
