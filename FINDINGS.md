# ctxloom — Architectural Review Findings

**Base:** `0f59fbae2399c35316cce04cb952e1a83599c6cf` (branch `release/0.7`) · **Review branch:** `docs/architecture-review`
**Scope:** 164,271 reviewable LOC · 694 files · 123 packages · 164 units (162 reviewed, 2 skipped as generated)
**Method:** phase 2 — one Opus reviewer per unit, every line read. phase 3 — 8 lens synthesizers over the 6 MB unit corpus. phase 4 — coordinator ranking + verification.
**Artifacts:** per-unit `~/.ctxloom/sessions/bent-mixed-able/arch-review/units/U*.md` · lenses `…/synthesis/0*.md` · raw threads `…/CROSSCUTTING.md` · this file is the register.

---

## STATUS as of `9492dd16` (2026-07-26)

The review was written against `0f59fbae`. **Remediation has landed since** — an 8-batch sweep plus five release-blocking fixes. Each affected item below is marked in place; nothing is deleted.

| item | state | commit |
|---|---|---|
| **T1** `deps upgrade` wipes `lock.yaml` | **RESOLVED** | `fd0d87d6` |
| **T2** `deny_tools` / `skills` dropped on the launch wire | **RESOLVED** | `40b49a7f` |
| **T4** manifest-less skill preimage is a constant | **RESOLVED** | `8d9da20c` |
| **T7** total-struct parity gate | **RESOLVED** | `40b49a7f` |
| **T13** ACP fs handlers serve any host path | **RESOLVED** | `73ea8d7f` |
| **X9** / `U061-F02` — `PreToolFallback` has no proto field | **RESOLVED** | `40b49a7f` |
| retraction fail-open on an unreadable lockfile (found *during* T1) | **RESOLVED** | `9492dd16` |
| the census (`docs/architecture/findings-index.md`) | **168 of 2,268 rows resolved** | `0e35e9b9` + the five above |

**T3, T5, T6, T8–T12, T14–T20 are UNTOUCHED.** So are the ~2,096 census rows no commit names — and `open` there means *no commit named that ID*, not *re-verified as still broken*.

Items the fixes themselves raised, which are **open** — see [Open — explicitly not established](#open--explicitly-not-established) at the end of this file for the full list.

---

## How to read this

Every finding carries a verification provenance. **This is the most important column** — the review's own headline claims moved in both directions under scrutiny.

| tag | meaning |
|---|---|
| **VERIFIED** | the coordinator re-read the source and confirmed it, independent of the agent that reported it |
| **REPRODUCED** | an agent executed it against a real binary and it behaved as described |
| **CONFIRMED** | a synthesizer verified it against source and stated its method |
| **CLAIMED** | asserted by a unit review, not re-verified — **do not act on these without checking** |

### A blind spot in this review's own method — added 2026-07-26

The per-unit reviews cannot see a **hand-mirrored struct that drops a field**, and this
was demonstrated rather than theorised.

`chatEventToJSON` (`internal/cli/run_structured.go`) mirrors `agent.ChatEvent` onto the
`--format json` stream the VSCode frontend consumes. It dropped **ten** fields, including
`sessionId` — the resume handle, so the frontend could not offer "continue this
conversation" at all. **U041 §3 reviewed those exact DTOs and returned "KEEP."**

The reason is structural: a reviewer reads the mirror struct and its converter *as a
pair*, and **they agree with each other perfectly**. The drop is only visible against the
*source* type, which is in another package and not part of that unit. No amount of care
inside the unit would have found it.

**Consequence for anyone using this register:** absence of a finding is not evidence of
correctness, most of all for hand-mirrored types and for anything whose correctness is
defined by a relationship to code the unit does not own. The remediation sweep has now
found four such mirrors — two in the corpus, one out of it, one already correct — and
every one was caught by a reflective class gate, never by reading.

That is also the argument for the gates over the fixes: nine of them now exist, and
several caught defects, and one caught *itself*, in code no unit review flagged.

---

### This file is the action list, NOT a census — read both

**Corrected 2026-07-25.** An earlier version of this section said "381 HIGH across 162 units" and let that stand in for the corpus. **That was wrong and it undersold the corpus by a factor of six.** The real totals, mechanically extracted:

| | count | resolved as of `9492dd16` |
|---|---|---|
| findings in the corpus | **2,268** | **168** |
| HIGH | 376 | 47 |
| MED | 999 | 30 |
| LOW | 871 | 91 |
| ranked items in THIS file | **20** | 5 (T1, T2, T4, T7, T13) |

The resolved counts are **mechanical** — a row is marked resolved iff a commit in `0f59fbae..HEAD` names its ID. They are a floor, not a measurement: a fix that closed a sibling's root cause without naming the row is not counted.

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
   **STATUS: DISCHARGED.** Confinement landed first (`73ea8d7f`), then `ChatStart.runtime` (`40b49a7f`). `internal/acp/fsconfine.go`'s `confineToWorkspace` gates both fs handlers against `ChatRequest.WorkDir`. The constraint held in the shipped order — kept here because it is the record of *why* the two commits are sequenced.
2. **Do not wire `extra_args` into argv before its allowlist exists** (S14). Today it is a documented "enforcement point" with zero readers — harmless. Wiring it without the allowlist makes it an injection point.
3. **S5's two halves must both land**; neither closes the other, and either alone reads as fixed.
4. **Do not add `--ignored` to `IsDirty` alone.**
5. **T1's guard (b) should land before or with (a).** Refusing to write an empty lockfile over a non-empty one closes the whole class; fixing only the fallback closes one trigger.
   **STATUS: DISCHARGED** — both landed together in `fd0d87d6`, guard first (`28f313f7`), then the trigger (`b51c7209`).

---

## Tier 1 — act on these

### T1. `deps upgrade` silently wipes `lock.yaml` and reports success — **VERIFIED** · **RESOLVED `fd0d87d6`**
Highest data-loss finding in the review. Every link re-read by the coordinator.

**RESOLVED.** Both halves landed, guard before trigger. (b) `LockfileManager.Save` reads back what is on disk and refuses two cases — empty over populated (`ErrLockfileWouldErase`, naming how many entries it protected) and **any** write over an unparseable lockfile (`ErrLockfileUnreadable`, naming the recovery, with no override: holds and retractions that cannot be read cannot be carried forward). `AllowEmpty()` is the opt-in for a caller that emptied the lock deliberately, and relaxes only the first refusal; the signature is variadic so no call site churned. All six call sites covered. (a) `deps upgrade` calls the config loader directly and fails loud. `U085-F01` (a corrupt lock degraded to empty and was then saved, clearing every hold and retraction) folded in — the degrade still happens but can no longer be persisted, and the corrupt file is left byte-identical.

**Raised by this fix and still OPEN:** partial closure narrowing still writes a non-empty result; `deps check` still reads via `loadConfigOrFallback`.

#### T1a. The trust gate failed OPEN on an unreadable lockfile — **RESOLVED `9492dd16`**
**Not from the original review** — escalated out of T1's own work and recorded here so it is not lost. `buildLockfileRetraction` degraded an unparseable `lock.yaml` to "nothing is retracted", so content a publisher had deliberately **withdrawn** was silently served again. No write is involved, so T1's write guard does not reach it; retraction exists for "this turned out to be harmful", so failing open inverts the control.

Now denies via `trust.Deny` + `trust.SourcePending`, recorded as `strictness.FailOnce(ClassTrust)`. **Scoped to remote refs**, not everything: the lockfile records only remote bundle entries, so an unreadable one conceals nothing about a local or builtin ref. One predicate, `retractable(ref)`, is shared by the gate and by `lockfileRetraction.Retracted` so they cannot drift. Absent ≠ corrupt — `Load` already maps `os.IsNotExist` to an empty lockfile with a nil error, so a project with no pins is untouched.

`remote_upgrade.go:35` uses `loadConfigOrFallback`, which on ANY config-load error returns a minimal empty config (`startup_helpers.go:38-39`). Empty config → no profile definitions → `closureRoots` empty (`depgraph.go:95-107`) → `proposed` and `unexpanded` both empty → the carry-forward guard at `upgrade.go:77` **never fires** → `Save(newActive)` at `:90` is **unconditional** and writes an empty lockfile, erasing every pin → `advanced == 0` → prints **"Everything is up to date."**, exit 0.

- **Root cause, one line:** a fault-tolerant fallback intended for read-only commands (its own comment names `deps check` and `search`) is used by a destructive one.
- **The author guarded the wrong case.** `upgrade.go:28-30` reasons explicitly about `Save(newActive)` erasing entries — and applies that reasoning to root-set narrowness and `unexpanded`, not to the empty closure.
- **Security-relevant, not just inconvenient:** per S12 retraction state is cleared by `deps upgrade`, so a wipe silently **un-retracts** previously retracted content.
- **Trigger is routine:** a config-load error, not an exotic state.
- **Fix:** (a) `deps upgrade` must refuse a fallback config and fail loud; (b) `Save` must refuse to write an empty lockfile over a non-empty one absent an explicit force. **Do (b) regardless** — it closes the class.

### T2. `deny_tools` and `skills` are dropped on the LAUNCH wire — **VERIFIED** · **RESOLVED `40b49a7f`**
Reported independently by three lenses (D1, W2, S7); reach settled by the coordinator.

**RESOLVED.** One proto change and one regen carried **eight** fields, not two — `ChatStart.runtime` (9), `ChatStart.resume_session_id` (10), `ManagedConfig.skills` (6), `ManagedConfig.deny_tools` (7), `Hook.pre_tool_fallback` (8), `ChatPermissionRequest.kind` (6), `ChatSessionInfo.session_id` (5), `ChatSessionInfo.resumable` (6). This closes T2, **X9**/`U061-F02`, `U059-F01`/`F02`, `U012-F03` and **T7** together.

The last three were found **by the parity gate, not by the review** — `session_id` and `resumable` are the return half of `resume_session_id`, so fixing only the request side would have shipped a half-wired feature.

Consequences closed: a configured `deny_tools` entry now reaches the backend on the launch path; `pre_tool_fallback` is no longer dead on Antigravity, so the delivered hook matches the grant its signed preimage covers; a container-bound `ctxloom acp` session no longer runs on the host while ctxloom announces container isolation; a delegated child's resume no longer starts fresh.

**No signature, hash, grant or countersignature changed, and no `ExecPreimageContract` bump** — the exec preimage already included `pre_tool_fallback` and is built host-side before `ManagedConfigToProto` runs, so the trust gate never hashed a wrong value. What was broken was the other half of the correspondence: the hook *delivered* had the flag cleared.

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

### T4. Manifest-less skill trust preimage is a constant, and verification is skipped on the same predicate — **REPRODUCED** · **RESOLVED `8d9da20c`**
`{"preimage":"ctxloom-exec/1","manifest":[]}`, sha `502727b7…` (executed), with `loader_skills.go:105` skipping verification on the identical condition (S2). Two independent failures of the same trust check, in one branch. Directly undermines the signed-context claim.

**RESOLVED** on `fix/skill-trust-preimage`, merged as `8d9da20c`. `BundleSkill.EffectiveManifest` is the one answer to "which files is this skill": the authored manifest when synced, otherwise one derived from the real source tree, failing closed on an unreadable/escaping tree. Both halves are closed — the `len(entry.Files) > 0` predicate is gone from the loader, so `VerifyExtractedManifest` runs on every skill against the same manifest the gate decided on. Review and the accepted-content snapshot render that same effective manifest, so the listing a user approves is the one the hash covers.

**S2's recommended remedy was deliberately not taken**, and the divergence is the finding's one open question: S2 says refuse to gate a manifest-less skill. The code says manifest-less is a legitimate shape — `ctxloom skill create` (`operations/skills.go:236`) emits exactly it, and `skill sync` is a later step. Refusing would break authoring for every project-local bundle, whose local-tier trust already auto-allows it. Deriving the preimage closes the hole *and* keeps the workflow, which is strictly better; but if the intent really is "a skill must be signed before it can be used", that is a product call that would reverse this. **No contract bump:** the payload's field set is unchanged, and `signing.ExecPreimageContract` is shared with MCP servers and hooks, so bumping it would mass-invalidate their untouched approvals as collateral. **Previously-recorded approvals of manifest-less skills are invalidated** — unavoidably, since they attested to a constant; they return to pending for one re-review rather than being migrated, because there is no honest way to re-point an approval-of-nothing at real bytes.

### T5. The 2026-07-24 retry storm is **NOT** fixed — **CONFIRMED**
The agent overturned its own first-pass conclusion. The gate bounds consecutive launch *failures*, but `noteLaunchAttached` **resets the counter on every attach** — so an attach-then-die child relaunches forever at `launchBackoff(0)` = **zero delay** (C12). Compounded by C13: plane-2 `agent_stop` never calls `cancelLaunch` (1 caller repo-wide), which is why "the loop outlived every stop issued against it."

This is the architecture behind the observed 846 zombies / 208 orphaned runners. Those specific symptoms are durably fixed; **the generating mechanism is not.**

### T6. Six of sixteen CI gates do not gate — **VERIFIED** (by exit code; 3 independently re-checked)
| gate | result |
|---|---|
| `complexity-check` | **exit 1** — red and ignored |
| `test-conformance` | **exit 1** *and in no workflow* — it caught a real claude-vs-codex divergence nobody is watching |
| `gen-schemas` (untagged) | **exit 0 having emitted zero bytes** — the *untagged* stub (`U003-F01`) is **untouched**. Separately **RESOLVED `20451f26`**: `just gen-schemas` now exits 1 on an empty target list, and `just gen-mcp-schemas` on an empty binding table |
| `validate` | ~~exit 0 validating 0 files~~ **RESOLVED `e479306b`** — targets are typed (the gitignored project config OPTIONAL, the three tracked `resources/*-config.yaml` REQUIRED), a zero-document run is an error, and each validated path is printed by name. A textbook CI-vs-local asymmetry: the file exists locally, so the same command looked like it worked |
| `gen-schemas-check`, `extract-defaults -check` | **do not exist** — despite comments citing them as protection |
| gofmt | **no gate anywhere**; 19 dirty files |

`gen-schemas` is this project's characteristic bug living inside its own CI. Enforcing and real: `test`, `gen-mcp-schemas-check`, `gen-docs-check`, `vet-integration`, and `CTXLOOM_REQUIRE_DOCKER=1` at `ci.yml:236`.
**Unknown:** `just lint` returned exit 3 on a host toolchain mismatch — genuinely undetermined, re-run in the devcontainer.

**The complexity gate also has a structural blind spot, independent of being red** — CLAIMED (phase-4 documentation agent): **lizard does not descend into func literals**, so the ~930-line `runCmd.RunE` closure (`cli/run.go:367-1300`) — the single largest body of logic in the CLI — is invisible to it even when the gate runs. Two separate problems: the gate does not run, *and* it could not see the worst offender if it did. Fixing only the red does not close this.

### T7. The one testing change with the highest yield — **CONFIRMED** · **RESOLVED `40b49a7f`**
A dropped field is an **absent statement**, and no coverage, mutation, or complexity metric can point at a line nobody wrote. The existing round-trip test asserts `req.MCPServers == back.MCPServers` — one *named field*, not `req == back`. MCPServer survives because it is the field the assertion names.

**One reflective total-struct round-trip helper (~1 day) across every hand-mirrored converter covers ~104 HIGH wire drops.** This is the highest findings-per-unit-of-work item in the review. Runner-up (~1 hour): invert the 6 tests that currently *pin* silent no-ops as intended behavior.

**RESOLVED, built and landed with T2.** The helper populates every field at every depth with distinguishable non-zero values, round-trips, and requires the whole struct back. It **names no field**, so a field added later is covered without anyone updating the test. It covers all **27** hand-mirrored pairs in `internal/lm/grpc`, with an AST-walking gate that fails when a pair is added without a sweep entry; two exclusions, each with a written reason and an anti-rot test.

**Verified non-vacuous, twice:** the sweep was run against unmodified source first and failed with ten subtests naming every dropped field; and independently by deleting the `Runtime` assignment and confirming `TestArch_ProtoConverters_MirrorEveryStructField/agent.ChatRequest` fails naming that field.

**The runner-up is NOT done** — the 6 tests that pin silent no-ops as intended behaviour are untouched. So is the reach beyond `internal/lm/grpc`: the helper covers that package's 27 pairs, not every hand-mirrored converter in the repo.

---

## Tier 2 — structural, schedule deliberately

- **T8. Nine MCP arguments are declared, accepted, and have zero effect** — CONFIRMED (W1). All 8 agentcoord ones are published as real in the *generated public* reference. Four artifacts agree (proto → generator → runner → docs); only the handler disagrees. **Fix = one generator assertion:** every projected input field must be read by its handler.
- **T9. The exit-0-on-failure family: five independent root causes**, not one and not thirty (R1–R5). R1 no exit-code policy for management commands (`strictness` is deliberately launch-only: 57 producers, 7 drains, all launch-path). R2 failure not representable in the return type. R3 see T3. R4 "absent" vs "unreadable" conflated. R5 empty input parses as valid — underlies the whole zero-payload family.
- **T10. Trust store designed fail-closed, implemented fail-open** (F6) — CONFIRMED.
- **T11. Six real import cycles deferred into external `_test` packages** — VERIFIED (L1): `coord↔cli/tui`, `termui↔cli/tui`, `transcript↔lm/grpc`, `shared/agent↔{claude,codex,kiro}`. **Four were invisible to every unit review** — each is only visible from outside a single unit. Zero production cycles.
- **T12. Engine identity enumerated in four rosters with four different memberships** — CONFIRMED (L3). `internal/operations` importing `claude`/`codex`/`kiro` is a literal ADR-0026 violation in the core.
- **T13. `internal/acp` fs handlers serve any absolute host path** — CONFIRMED (S3). Was masked by the `ChatStart.runtime` drop; that mask is now gone (`40b49a7f`), which is why the ordering mattered. **See fix-ordering constraint 1.** **RESOLVED `73ea8d7f`**: one boundary, `confineToWorkspace` in `internal/acp/fsconfine.go`, applied **before** the fs-upstream branch in both handlers so the editor-chained axis is confined too; symlinks resolved on both root and candidate including dangling links; unresolvable root, unreadable ancestor, stat error and symlink loops all deny; relative paths refused rather than resolved, per the ACP schema. Root is `agent.ChatRequest.WorkDir`, the same value handed to the engine subprocess as `cmd.Dir`, so the boundary and the engine's cwd cannot drift. 18 confinement tests. **Still open, filed as `loud-guide`:** `internal/acpagent/fsupstream.go`'s relay is itself unconfined and its unix socket is locally callable; TOCTOU between check and syscall (needs `openat2` `RESOLVE_BENEATH`); the unconditional `Fs` capability advertisement.
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

### Raised BY the remediation, still open (added 2026-07-26)

These are not from the review — the implementing agents surfaced them while fixing something adjacent, and each was deliberately **not** absorbed into the fix it was found in. They are open work, recorded here so they survive.

| item | raised by | state |
|---|---|---|
| `internal/acpagent/fsupstream.go`'s relay is itself **unconfined**, and its unix socket is locally callable — so the confinement T13 added to `internal/acp` can be reached around | T13 `73ea8d7f` | **open**, filed as taskloom `loud-guide` |
| TOCTOU between the confinement check and the syscall — needs `openat2` `RESOLVE_BENEATH` | T13 `73ea8d7f` | **open**, same task |
| The `Fs` capability is advertised unconditionally | T13 `73ea8d7f` | **open**, same task |
| **Partial closure narrowing still writes** a non-empty result — the T1 guard refuses empty-over-populated, not populated-but-smaller | T1 `fd0d87d6` | **open** |
| **`deps check` still reads via `loadConfigOrFallback`** — the same fault-tolerant fallback whose use by a destructive command *was* T1. `deps check` is read-only today, so it is correct-by-accident, not by construction | T1 `fd0d87d6` | **open** |
| T4 **diverges from S2's recommended remedy** (S2: refuse to gate a manifest-less skill; the fix: derive a real preimage instead). Recorded as a **product call**, not a defect — reversing it is a decision, not a bug fix | T4 `8d9da20c` | **open decision** |
| T4 **invalidates previously-recorded approvals of manifest-less skills.** No honest migration exists — they attested to a constant. They return to pending for one re-review | T4 `8d9da20c` | **accepted consequence**, shipped |
| A **stale orphaned narration block** in `trust_surface.doc.md` | sweep / `f48ec814` | **open** — see below |
| T7's runner-up (invert the 6 tests that pin silent no-ops) and its reach beyond `internal/lm/grpc` | T7 `40b49a7f` | **open** |
| One batch refutation (`confload.Merge`'s swallowing branches are unreachable) named **no finding ID**, so the census cannot key on it | sweep `0e35e9b9` | **untracked** |

---

## Markup instructions

Per the standing decision, **no taskloom tasks were filed from this review.** Mark this file up — strike what you reject, annotate what you want — and tasks get seeded from your markup, not from the register wholesale.
