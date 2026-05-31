# Bundle Review branch — retrospective and what's next

> **Status:** implemented end-to-end on `feat/bundle-mcp-tools` (2026-05-25); audit + post-merge coverage push landed 2026-05-26/27. **Living document** — keep in sync with conversation decisions and follow-up PR progress.

The branch landed bundle review on startup with SHA-keyed reads, a native task layer replacing flesler/mcp-tasks, harp-named sessions with a pre-launch picker, an MCP footprint cut (~46 → 18 tools + 12 resources), an embedded tasks bundle, hook command path-rewriting, and a coverage push that brought the aggregate to ~60%. The pre-implementation design lived as Phase 1–4 specs; the code is now the source of truth. Anything genuinely subtle is captured below.

---

## What shipped (retrospective)

### Where each phase landed

| Phase | Symbol(s) |
|---|---|
| 1.1 BundleReader | `internal/remote/bundle_reader.go::BundleReader` (bytes); `internal/operations/bundle_reader.go` (factory) |
| 1.2 Drop bundle fs writes | `internal/remote/pull.go::Pull` — `switch opts.ItemType { case ItemTypeBundle: … }` skips MkdirAll+WriteFile; `PullResult.Content` carries bytes for in-process callers |
| 1.3 Migrate read sites | All `operations.bundleLoader(cfg)` and `*Loader` constructions now delegate to `Config.SeededBundleLoader(...)`. Seeded bundles surface via `bundles.WithSeededBundles` (`internal/bundles/bundles.go`) |
| 1.4 Legacy cleanup | `internal/operations/legacy_cleanup.go::PurgeExtractedBundles`; invoked from `cmd/mcp_server.go::startup` |
| 2.1 Diff | `internal/operations/bundle_diff.go::DiffLockfiles`, `BundleChangeSet`, trust filter |
| 2.2 TrustBundles | `internal/remote/types.go::Remote.TrustBundles`; persisted by `registry.save()`; mutated via `Registry.SetTrustBundles` |
| 2.3 Pending lockfile | `internal/remote/lockfile.go::WithPendingLockfile`; `Puller.WithBundleLockfileTarget` routes bundle writes to pending; `operations.computeBundleChanges` runs the diff post-sync |
| 3.1 + 3.3 Review state, deferred hooks | `cmd/bundle_review.go::bundleReviewState`; `cmd/mcp_server.go::applyHooksIfNotPending` |
| 3.2 + 3.4 Initialize wiring + template | `cmd/mcp_server.go::startup` calls `s.handleSyncChanges`; `Instructions` set on `NewServer`; template in `cmd/bundle_review.go::renderReviewTemplate`; env-var bypass `CTXLOOM_AUTO_APPROVE_BUNDLES=1` |
| 4.1 + 4.2 Gate + prepend | `cmd/bundle_review_middleware.go` |
| 4.3 Review tools | `cmd/mcp_tools_review.go` — `acknowledge_bundle_review`, `decline_bundle`, `show_bundle_verbatim`, `trust_remote` |

### Tests

| Suite | File |
|---|---|
| Diff (incl. pinned-active suppression) | `internal/operations/bundle_diff_test.go` |
| Pending lockfile lifecycle + pin/unpin | `internal/operations/lockfile_pending_test.go` |
| Legacy cleanup | `internal/operations/legacy_cleanup_test.go` |
| Review state + template + gate allowlist | `cmd/bundle_review_test.go` |
| Structural bundle diff (`diffBundleYAMLs`) | `cmd/mcp_tools_review_test.go` |
| Wire-protocol gate behavior (init-pending → blocked → ack/decline → unblocked, instructions carry protocol text, env-var bypass, tools/list lists review tools) | `cmd/mcp_review_integration_test.go` |

### Implementation notes

The codebase that received this work differed from the plan's mental model in two ways. The substantive guarantees (hash-per-bundle reads, review gate, deferred hooks) are intact.

1. **There is no per-bundle "extracted dir."** Bundles ship as single YAML files; `pull.Pull` previously copied them to `.ctxloom/cache/bundles/<remote>/<path>.yaml`. "Extraction" in Phase 1.2 was interpreted as removing that file copy. `operations.PurgeExtractedBundles` keys on `_source.SHA` presence so locally-authored bundles survive.

2. **`BundleReader` is split.** `internal/remote/bundle_reader.go` owns the bytes-only fetch (no `bundles` import). `internal/operations/bundle_reader.go` provides `NewBundleReaderForConfig` and `NewBundleReaderForLockfile` (which `show_bundle_verbatim` uses against pending). The loader-seeding pipeline lives on `(*config.Config).SeededBundleLoader(...)` in `internal/config/config.go` so `internal/lm/backends` and `internal/config` callers can build seeded loaders without depending on `internal/operations` (cycle).

The plan referenced legacy line numbers in `cmd/mcp.go` (`handleInitialize`, `handleToolsCall`). Those locations are gone after the SDK migration. The SDK equivalents used:

- `mcp.ServerOptions.Instructions` (set on `NewServer`) for the initialize-time injection (Phase 3.2). The text lives in `cmd/bundle_review.go::reviewInstructionsBlock`.
- `Server.AddReceivingMiddleware` for the gate + prepend chokepoint (Phase 4.1/4.2). The discriminator is `method == "tools/call"`.
- `cmd/bundle_review_middleware.go` carries the `allowedDuringReview` map and the `reviewMiddleware` function.

### Settled design choices (for the record)

- **Storage model:** version-aware getter at repo layer (`BundleReader → cacheFetcher → GitCloneFetcher`). No fs extraction.
- **Injection sites:** both `initialize.instructions` + sticky tool-result prepend.
- **Change scope:** new bundles + content-modified bundles (SHA diff). Profile/hook deltas excluded.
- **Trust persistence:** per-session approve default; per-remote trust persisted via the `TrustBundles` config flag.
- **Non-interactive bypass:** explicit env var `CTXLOOM_AUTO_APPROVE_BUNDLES=1` with stderr warning. No TTY-magic.
- **Block scope:** B3 (hard block — only allowlisted tools callable until acknowledged).
- **Template authoring:** fixed verbatim template, not natural-language paraphrase.
- **Decline persistence:** per-session only.
- **Decline of NEW bundle:** don't install at all.

---

## Open issues — audited 2026-05-26

Original assumptions and what the code audit found.

- **Bundle review gate ordering.** *Verified.* Built-in bundles bypass the review gate **structurally**, not via an allowlist: built-ins are loaded directly from `resources/builtin_bundles/` by `resolveBuiltinBundleHooks` (`internal/config/config.go:1241`) and never enter the lockfile, so `DiffLockfiles` (`internal/operations/bundle_diff.go:61`) can't produce a `BundleChange` for them. Remote bundles take the puller path, land in `lock.pending.yaml`, and feed the review state. SCM tagging (`builtin:<name>`, `bundle:<source>`) is consumed by `isCtxloomManagedHook` at reconcile time as defense-in-depth.
  - **Latent gap (dormant):** `ResolveBundleMCPServers` (`internal/config/config.go:1132`) doesn't iterate built-ins, only profile-referenced bundles. The single shipped built-in (`tasks.yaml`) declares no MCP servers, so this is harmless today; any future built-in that ships MCP servers would silently drop them.
- **OSC2 terminal title.** *Spec-consistent, smoke-test still pending.* Code at `cmd/run.go:315` emits `\033]2;ctxloom · <harp>\007` gated on `isInteractiveTerminal()`. Microsoft's console-virtual-terminal-sequences spec lists OSC2 as supported on Windows Terminal, matching the code comment's claim. Failure mode is cosmetic (literal escape on a dumb terminal); no functional impact. Defer the actual Windows Terminal smoke-test until someone runs ctxloom there.
- **Picker `d<N>` resilience.** *Verified, behavior stronger than the original claim.* `shellOutDistill` (`cmd/run.go:115`) calls `os.Executable()` on **each keystroke**, not at picker construction. If it errors, falls back to bare `"ctxloom"` → PATH lookup. The picker absorbs distill failures inline (`internal/sessions/picker.go:193`) and keeps looping.
  - **Doc nit (this file's previous claim):** prior text said the failure mode was "documented in the picker comment". It isn't. The `shellOutDistill` doc comment describes the shell-out shape but doesn't mention the binary-moves-mid-session case. Fix when convenient — either by adding the line to the comment, or by accepting that the fallback chain (bare → PATH) makes the failure rare enough to skip.
- **Backend session-id binding precedence.** *Verified live 2026-05-28; the transcript-scan layer was removed and the real forward-bind bug fixed.* Now **two** layers, both short-circuiting when `entry.SessionID != ""` *before* calling `BindSession`: the SessionStart hook (`bindSessionFromPayload`, `cmd/session_cmd.go`) and compact-time (`internal/memory/compactor.go`). The third "transcript scan" layer (`discoverSessionByHarpName` + scoped-instructions marker) was deleted — it guessed a binding from jsonl content; `runSessionDistill` now errors clearly when no `session_id` is bound. `session_cmd_test.go::already_bound_is_idempotent` pins the SessionStart caller's no-op; storage-layer defense-in-depth (`BindSession`, `internal/sessions/index.go`) no-ops on any already-bound SessionID, pinned by `TestBindSession_FirstBindWinsOnDifferentID`.
  - **Root cause of the forward-bind failure (fixed).** The SessionStart hook never fired for `ctxloom run` sessions: the Setup path (`BaseLifecycle.MergeConfigHooks`) wrote `settings.json` *before* launch but never resolved bundle-shipped hooks, so `session bind` was absent; only the MCP-startup `operations.ApplyHooks` added it — too late, after the backend had loaded its hooks. Fix: a single shared assembler, `backends.AssembleManagedHooks`, builds the complete managed set (config + default-profile + bundle + context-injection) and **both** writers route through it, so the remove-then-add reconcile in `WriteSettings` can't drop a hook one writer assembled but the other didn't. This also closed two latent siblings: a profile-shipped SessionStart hook would have hit the same drop-on-clobber class, and `ApplyHooks` aliasing `freshCfg.Hooks` in its per-backend loop duplicated bundle/inject hooks on the second backend. Verified end-to-end: a fresh `ctxloom run` on the fixed binary auto-bound `session_id` + `transcript_path` with no manual step. Pinned by `TestAssembleManagedHooks_{MatchesSetup,IncludesProfileSessionStartHook,DoesNotMutateConfig}`.

### Latent gaps surfaced by the audit (resolved 2026-05-27)

- **Built-in bundles dropped from `ResolveBundleMCPServers`** — *fixed.* `resolveBuiltinBundleMCPServers` mirrors `resolveBuiltinBundleHooks`; any future built-in shipping MCP servers is now picked up with `SCM=builtin:<name>`. Pinned by `TestResolveBuiltinBundleMCPServers`.
- **Picker `d<N>` doesn't catch `(deleted)` suffix** — *fixed.* `cmd/run.go::resolveSelfExecutable` strips the suffix, stat-verifies the result, and falls back to bare `"ctxloom"` (PATH lookup) when either step fails. Five unit tests cover the matrix.

### Trust + acknowledge reconciliation (fixed 2026-05-28)

- **Trusted bundles were orphaned in pending.** Sync routes every bundle write to `lock.pending.yaml`; `DiffLockfiles` filtered trusted remotes *out of the review changeset*, but nothing promoted them into active — so trusted bundles sat in pending forever and their fragments never materialized. *Fixed:* `operations.PromoteTrustedPendingBundles` lifts trusted-remote pending entries straight into active (pin overrides trust), called from `SyncDependencies` and `PendingBundleChanges`. Trust now truly bypasses the gate. Pinned by `TestPromoteTrustedPendingBundles`.
- **`acknowledge_bundle_review` desynced from disk.** The handler keyed off in-memory `s.review`, which is only populated at MCP startup. A CLI `ctxloom remote sync` while the server is running (or trusted-orphan leftovers) left the file holding entries the tracker never saw → "No bundle review pending" despite a non-empty `lock.pending.yaml`. *Fixed:* `handleAcknowledgeBundleReview` treats the pending file on disk as authoritative. Pinned by `TestHandleAcknowledgeBundleReview_ReconcilesFromDisk`.

---

## Coverage gaps

Current state (raw, post-2026-05-27 coverage push):

| Package | Coverage | Why it's not 100% |
|---|---|---|
| `cmd` | 23.8% (was 13.4%) | list/show/view/mutation/diff/create/edit/delete/export/import surfaces are all extracted and tested; remainder is largely cobra wiring + auth-bearing network publish |
| `internal/lm/grpc` | 43.9% | most uncovered is generated protobuf (filtered out by `.coverignore`); the remaining gap is hashicorp/go-plugin subprocess machinery |
| `internal/lm/backends` | 74.9% (was 68%) | claude/gemini history surfaces fully tested; codex now has the same afero seam + tests; remaining is `claudecode.go::Setup/Execute/Cleanup` which wraps real `exec.Cmd` (explicit anti-goal — see "What stops us going further" below) |
| `internal/sessions` | 84.8% (was 82%) | incidental gain from `BindSession` defense-in-depth + test |
| `internal/operations` | 84.1% (was 84.0%) | gained from pin/unpin + diff-suppression tests |
| `internal/config` | 77.5% (was 78.3%) | new `resolveBuiltinBundleMCPServers` adds an uncovered "list-builtins errored" branch; the happy path is covered |
| `internal/ptyrunner` | 70.7% | PTY syscalls — genuinely heroic, deferred indefinitely |
| `internal/filelock` | 75% | edge cases on contention; low-value |

### Wins landed this session

| Target | Before | After | Change |
|---|---|---|---|
| `cmd/config.go` helpers (renderConfigYAML, resolveConfigSection, renderConfigSection) | untested | 100% on extracted surface | +tests |
| `cmd/profile.go` helpers (renderProfileList, renderProfileShow, applyProfileMutations + writeBulletList) | untested | extracted + tested across the 10-branch mutation matrix | +tests |
| `cmd/bundle.go` helpers (cleanDistilledOutput, isStructuredContent, buildSiblingContext; renderBundleList/Show + per-entry helpers; renderBundleFragmentList/PromptList; parseBundleViewRef, renderBundleViewItem, writeViewContent, lookupBundleMCP) | untested | extracted + tested | +tests |
| `cmd/remote_update.go` (shortSHA, classifyPullError; analyzeBundleReferencesWithFS afero seam) | untested | extracted + tested across orphan/missing/invalid branches | +seam, +tests |
| `internal/lm/backends/codex.go` | 0% on history surface (no afero seam) | seam added; 14 hermetic tests covering JSONL parse, YYYY/MM/DD walk, all entry types | +seam, +tests |
| `internal/lm/backends/claude_capabilities.go` registry methods | 0% (registry silently bypassed the fs seam) | constructor fix + 7 tests | +bug fix, +tests |
| Bundle review UX: `pin_bundle` / `unpin_bundle` / `approve_remote_pending` MCP tools; `Pinned` flag on `LockEntry`; `DiffLockfiles` skips pinned; `show_bundle_verbatim` returns structural diff via `diffBundleYAMLs` | feature didn't exist | shipped + tested at unit and template-allowlist layers | +feature, +tests |
| `cmd/bundle.go` create/import/export (`writeBundleSkeleton`, `importBundleFile`, `exportBundleFile`) | untested | afero-seam'd + 15 tests covering happy/missing/malformed/overwrite/dispatch | +seam, +tests |
| `cmd/bundle.go` edit/delete (`applyBundleEdits`, `deleteBundleFile`, `stdinConfirmer`) | untested | mutation matrix extracted + confirm seam'd; 20 tests | +seam, +tests |

### Cheapest remaining wins

None standing. The bundle.go create/edit/delete/export/import RunE bodies all have afero seams as of 2026-05-27. What's left is either explicit anti-goal territory (see below) or genuinely covered.

### What stops us going further

- **PTY syscalls** in `ptyrunner` — platform-conditional, requires real terminals or significant fakery
- **End-to-end LLM-driven flows** (`compact_session` against a real plugin) — would need a fake-plugin server fixture
- **`internal/lm/grpc::NewPluginClient`** still has hashicorp/go-plugin internals we don't mock; the cheaply-mockable surface is already covered via `dialPluginConnection`
- **`exec.Cmd` lifecycle** in claudecode.go (Setup/Execute/Cleanup) — wrapping `exec.Cmd` is an anti-goal: too thick a seam for too little signal, since the value is the actual subprocess behavior

---

## Pattern catalog for future expansion

Repeatable techniques the coverage push established. Use these for any subsequent gap-closure work:

1. **IoC seam over `exec.Command`** — `var execCommand = exec.Command`. Tests override; production unchanged. Example: `cmd/run.go::shellOutDistill`.
2. **Pure-helper extraction from cobra RunE** — pull decision logic out; cobra wrapper composes flags into a struct and delegates. Example: `cmd/run.go::resolveResumeIntentWith` + `resumeFlags`.
3. **Interface mock for third-party concrete types** — wrap with our own interface, swap in tests. Example: `internal/lm/grpc::pluginConnection` over `*plugin.Client`.
4. **Afero filesystem injection** — already widespread; just exercise more cases against the fake fs. Propagate to nested constructors (see the `WithClaudeSessionFS` → registry fix on 2026-05-27).
5. **In-process MCP handler tests** — `&ctxServer{}` + `withProjectDir(t)`, no subprocess. Example: `cmd/mcp_resources_test.go`.
6. **Wire-protocol subprocess tests** — only when SDK serialization or registration is the contract being pinned. Example: `cmd/mcp_resources_integration_test.go`.

Avoid these (genuine heroics):
- Subprocess tests for code that's not protocol-shaped
- PTY/TTY emulation
- End-to-end tests requiring a real LLM
- Wrapping `exec.Cmd` to test subprocess lifecycle

---

## Deferrals

Speculative follow-ups are captured as individual ADRs under [`docs/adr/`](adr/README.md) — one decision per file, each with status, context, decision, and a revive trigger. The bundle-review-specific deferrals are ADRs [0001](adr/0001-skip-bundlereader-cache.md) – [0005](adr/0005-skip-fetcher-fixture-tests.md); the tasks/sessions/distribution deferrals are ADRs [0006](adr/0006-skip-task-link-session.md) – [0013](adr/0013-keep-tasks-bundle-embedded.md).

---

## Reference: what landed on this branch (commit map)

For navigation; the rationale behind any decision lives in its commit message.

```
ea53d01 fix(hooks): rewrite bare `ctxloom` in bundle hooks to absolute path
4b3a922 test: cmd/init helpers — gitignore upkeep + config template
eba058b feat: dial seam in lm/grpc + helper extracts in cmd/hook_inject_context
f99c321 chore: apply .coverignore filter to canonical `just test` coverage
fc19001 test: lm/grpc 13% → 37%, lm/backends 67% → 68%, fix nil-deref bug
b38f202 test: ratchet resources to 87% and gitutil to 87% (was 33% and 42%)
6e00508 test: mock exec.Command + wire-protocol resources integration
d37237b test: retrofit IoC seams + unit coverage for cmd/ surfaces
37681b2 feat(sessions): replace time-window matcher with transcript-content scan
949bcc7 chore(sessions): remove first-tool-call bind middleware
fc59b5e feat(sessions): SessionStart hook is now the primary bind path
5cf7642 feat(sessions): time-window fallback for un-bound harps + ended_at logging
762278e docs: collapse plan from 674 lines to 104 — what-shipped record
fb81825 chore: trim Phase 4 leftovers — stub files, env-var hack, hidden hook commands
e2f1712 feat(sessions): restore force-distill path
99792cc feat(run,sessions): OSC2 window title + regression tests for hook dedup and builtin bundles
299470e chore(mcp): prune dead handler closures and input/result types from Phase 4
f414574 feat(sessions): plans.md split + browse_remote templated resource
3b6a7bf fix(hooks): recognize all ctxloom-managed hooks for dedup, not just inject-context
7ebe82c fix: drop picker `d<N>` distill keystroke + repair Phase 4 tests  (reverted by e2f1712)
c36b387 feat(bundles): ship core ctxloom bundles in the binary via go:embed
b66eb5e feat(sessions): load_session reads harp essence directly
2be2c6a docs: correct surviving-tool count after Phase 4 verification
15a5d94 feat(mcp): migrate listings to resources, remove their tools (Phase 4 Lever A)
7200da0 feat(mcp): demote 15 write tools to CLI-only (Phase 4 Lever B)
f89ad57 fix(bundle): drop explicit `.*` matcher from tasks bundle post_file_edit hook
9be0f8d feat(mcp): resources framework + 3 starter resources (Phase 4.1 partial)
e3a195d feat(memory): compactor writes essence under harp-dir layout (Phase 3.6)
1d2b418 feat(sessions): ctxloom session CLI surface + Rename/Forget index ops
0ae387a feat(sessions): resume by harp + frontmatter summaries + hud display
2679ece feat(run): wire pre-launch resume picker into ctxloom run
cce0a2a feat(sessions): pre-launch resume picker with per-row checkboxes
f0e7d48 feat(sessions): harp-named session index + pre-launch assignment
f1b886f feat(tasks): plan-stamping hook + draft ctxloom-default-tasks bundle
599d699 feat(bundles): ship hooks declaratively from bundle YAML
303170f feat(tasks): file-backed task store + MCP tools + ctxloom tasks CLI
b287dac feat(harp): native Go port of harp-core for ID generation
c0605d7 docs: draft ctxloom-tasks replacement plan
```
