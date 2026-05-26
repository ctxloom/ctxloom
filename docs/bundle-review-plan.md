# Bundle Review on Startup — Implementation Plan

> **Status:** implemented end-to-end on `feat/bundle-mcp-tools` (2026-05-25). All four phases landed in a single sweep on top of the SDK migration. Build and `just test` green.
> **Living document** — keep this in sync with conversation decisions and PR progress.

## Implementation notes (post-execution)

The codebase that received this work differed from the plan's mental model in two ways. Both adjustments are documented here for future readers; the substantive guarantees (hash-per-bundle reads, review gate, deferred hooks) are intact.

1. **There is no per-bundle "extracted dir."** Bundles ship as single YAML files; `pull.Pull` previously copied them to `.ctxloom/cache/bundles/<remote>/<path>.yaml`. "Extraction" in Phase 1.2 was interpreted as removing that file copy. `operations.PurgeExtractedBundles` keys on `_source.SHA` presence so locally-authored bundles survive.

2. **`BundleReader` is split.** `internal/remote/bundle_reader.go` owns the bytes-only fetch (no `bundles` import). `internal/operations/bundle_reader.go` provides `NewBundleReaderForConfig` and `NewBundleReaderForLockfile` (which `show_bundle_verbatim` uses against pending). The loader-seeding pipeline lives on `(*config.Config).SeededBundleLoader(...)` in `internal/config/config.go` so `internal/lm/backends` and `internal/config` callers can build seeded loaders without depending on `internal/operations` (cycle).

The plan referenced legacy line numbers in `cmd/mcp.go` (e.g. `handleInitialize`, `handleToolsCall`). Those locations are gone after the SDK migration. The SDK equivalents used:

- `mcp.ServerOptions.Instructions` (set on `NewServer`) for the initialize-time injection (Phase 3.2). The text lives in `cmd/bundle_review.go::reviewInstructionsBlock`.
- `Server.AddReceivingMiddleware` for the gate + prepend chokepoint (Phase 4.1/4.2). The discriminator is `method == "tools/call"`.
- `cmd/bundle_review_middleware.go` carries the `allowedDuringReview` map and the `reviewMiddleware` function.

## Where each phase landed

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

## Tests

| Suite | File |
|---|---|
| Diff | `internal/operations/bundle_diff_test.go` |
| Pending lockfile lifecycle | `internal/operations/lockfile_pending_test.go` |
| Legacy cleanup | `internal/operations/legacy_cleanup_test.go` |
| Review state + template + gate allowlist | `cmd/bundle_review_test.go` |

Wire-protocol integration test extension (the plan's "extend test from commit `87ee531`") and the full `bundle_reader_test.go` (which requires a real git clone cache fixture) are deferred follow-ups.

---

## Problem

ctxloom syncs bundles from remote sources on MCP server startup. Bundle content can contain prompt injection that activates as soon as the agent reads it. Today, new and modified bundles enter the agent's context without any user awareness — there's no point at which the user is told "these bundles changed since last session, want to review them?"

## Goals

1. Detect new/modified bundles after `SyncOnStartup`.
2. Hard-block bundle-touching tools until the user has reviewed.
3. Present a fixed-template review prompt verbatim, dual-injected (MCP `initialize.instructions` + sticky tool-result prepend).
4. Let the user approve, decline (keep prior SHA), or trust a remote — per-bundle or in bulk.
5. Surface friction up-front (never silent auto-approval based on heuristics).
6. Provide an explicit env-var bypass for non-interactive runs.

## Design summary

- **Sync = git fetch into clone cache.** No extraction step. The local git clone cache *is* the storage (see commits `7239b65`, `969d9c0`).
- **Lockfile records active SHA per bundle.** That's the only ctxloom-side state for "which version is in use."
- **Bundle reads go through a `BundleReader`** that calls `Fetcher.FetchFile(owner, repo, path, sha)` on demand. SHA comes from lockfile, so reads are version-pinned automatically.
- **Decline** = lockfile keeps the prior SHA. The new SHA is in the git cache (unreferenced, harmless) until a future approve.
- **Approve** = lockfile advances. Same Fetcher call now returns the new content.
- **Local bundles** (project-authored, no remote) keep their fs path and bypass the reader.

### Why this shape

- Rollback of fs content rejected as unclean (overwrites + race window).
- Content-addressed extraction (`bundles/<name>@<sha>/`) rejected as unnecessary fs cruft — git is already content-addressed; exposing that is cleaner than mirroring it on fs.
- Per-session decline persistence (no permanent pin); user can promote to an explicit `Reference.ContentVersion` pin later if they want.
- Hard-block (B3) chosen over soft-block — friction surfaces immediately; gated tools are an explicit allowlist.

## Existing infrastructure to leverage

- `internal/remote/git_clone_fetcher.go:46` — `FetchFile(ctx, owner, repo, path, ref)` reads any file at any ref from the local clone. Already local-only (commit `969d9c0`).
- `internal/remote/cached_fetcher_factory.go:16` — `NewCachedFetcherFactory` wraps fetchers so all content reads route through `cache.EnsureRef → GitCloneFetcher`. `BundleReader` must use this factory; never the raw `DefaultFetcherFactory` (API path).
- `internal/remote/lockfile.go` — `Lockfile{Bundles, Profiles}` keyed by ref; each `LockEntry` carries `SHA`. Diff target.
- Existing pinning landscape: `Remote.Version` (schema dir), `Reference.ContentVersion` (explicit content pin), `LockEntry.SHA` (recorded fetch). Decline integrates by keeping the lockfile SHA stable.

---

## Phase 1 — Remove extraction; route reads through `BundleReader`

Today, sync extracts bundle content from the clone cache into on-disk active paths. Replace with on-demand fetching.

### 1.1 New `internal/remote/bundle_reader.go`
```go
type BundleReader struct {
    registry *Registry
    factory  FetcherFactory  // must be NewCachedFetcherFactory(cache)
    lock     *Lockfile
}
func (r *BundleReader) ReadFile(ctx, bundleName, relPath string) ([]byte, error)
func (r *BundleReader) ListFiles(ctx, bundleName, relDir string) ([]DirEntry, error)
```
Internals: lookup `lock.Bundles[bundleName]` → derive (remote URL, bundle path, sha) → resolve `Fetcher` via factory → `FetchFile(owner, repo, path+relPath, sha)`. Local-only bundles (no lockfile entry pointing to a remote) fall through to existing fs read.

### 1.2 Delete extraction in `internal/remote/pull.go`
Pull keeps clone-cache update behavior. Lockfile update still happens. Active fs writes for bundle content are removed.

### 1.3 Migrate read sites
Per the code map (`cmd/mcp.go`):
- `assemble_context` (1566), `get_fragment` (1494), `get_prompt` (1605), `search_content` (1626)
- Any direct fs reads in `internal/operations/helpers.go`

Route each through `BundleReader`. Local bundles unchanged.

**Coordination with `EnsureAutoProfile`** (added by PR #2, lives in `internal/operations/profiles.go`, called from `AssembleContext`): the auto-profile synthesizer enumerates installed bundles by walking `.ctxloom/bundles/<name>/` on disk. Once extraction is removed in 1.2, that directory is gone — the source of truth for "installed bundles" becomes the lockfile (`lock.Bundles` keys). `EnsureAutoProfile` must be updated to read from the lockfile (and treat local-only bundles separately, since they still live on disk). Tracked together with the rest of 1.3 — same PR, same review surface.

### 1.4 Legacy extracted-dir cleanup
The old extraction path `.ctxloom/bundles/<name>/` is now redundant — `BundleReader` serves the same bytes from the git clone cache. New on-disk state (`lock.pending.yaml`) lives next to `lock.yaml`, *outside* `.ctxloom/bundles/`, so there is no name collision.

On startup: unconditionally `rm -rf .ctxloom/bundles/` if it exists, log a one-line stderr notice (`ctxloom: removed legacy extracted bundle dir .ctxloom/bundles/`), and continue. No prompt, no env-var gate, no first-run-detection marker — the rename of the new state out of the old path is what makes this safe.

### 1.5 (Deferred) in-memory read cache
`BundleReader` could cache `(bundleName, sha, relPath) → bytes` for the session. Add only if profiling shows it.

---

## Phase 2 — Pending lockfile + diff

### 2.1 `internal/operations/bundle_diff.go`
```go
type BundleChangeSet struct {
    Added    []BundleChange  // not in prev lockfile
    Modified []BundleChange  // SHA differs from prev
}
type BundleChange struct {
    Name, Remote, OldSHA, NewSHA string
    Size int64
}
func DiffLockfiles(prev, curr *Lockfile, reg *Registry) *BundleChangeSet
```
Filter out entries whose source remote has `TrustBundles: true`.

### 2.2 Remote schema — `internal/remote/types.go:11-14`
Add `TrustBundles bool \`yaml:"trust_bundles,omitempty"\`` to `Remote`. Round-trip via `internal/remote/registry.go:108-120`.

### 2.3 Split lockfile: active + pending
- `lock.yaml` — active (what `BundleReader` reads against).
- `lock.pending.yaml` — proposed updates from the latest sync.

`SyncOnStartup` writes new entries into pending if SHA differs from active, or if active doesn't have the bundle. Sync no longer touches `lock.yaml` for changed bundles. Result returns `BundleChangeSet`.

- For modified bundles: `BundleReader` keeps reading old content (active SHA) until approve.
- For new bundles: not in active lockfile → `BundleReader` returns "bundle not installed" until approve.

---

## Phase 3 — MCP review state + initialize wiring

### 3.1 `mcpServer` struct — `cmd/mcp.go:372`
```go
type mcpServer struct {
    ...existing...
    review *bundleReviewState
}
type bundleReviewState struct {
    mu      sync.Mutex
    pending *operations.BundleChangeSet
}
```
`pending == nil` means no review needed. Clearing `pending` is the "acknowledged" signal.

### 3.2 `handleInitialize` — `cmd/mcp.go:521`
After `SyncOnStartup`:
- If `result.Changes` non-empty AND `CTXLOOM_AUTO_APPROVE_BUNDLES != "1"`:
  - Populate `s.review.pending`.
  - **Defer hook application** — do not run the existing post-sync hook phase yet.
  - Append review protocol text to `initialize.instructions`.
- If env var set: log stderr warning listing changes, merge pending into active immediately, apply hooks as today, append `{ts, sha_list, remote_list}` to a `last_auto_approved` entry in the lockfile for audit, skip review state.

### 3.3 Deferred hook application
Bundle-shipped hook scripts can execute arbitrary code, so they MUST NOT run while bundle content is unreviewed. State machine:

- `pending != nil` → hooks deferred (existing apply-hooks startup phase is skipped this run).
- `pending == nil` at startup → hooks applied as today.
- `pending` transitions to nil via `acknowledge_bundle_review` (merge → apply hooks against the NEW active set), `decline_bundle` with no entries left (apply hooks against the still-OLD active set — no new code introduced), or `trust_remote` clearing the last entry (same as acknowledge for those bundles, then apply hooks).

Hook application is therefore triggered from exactly two places: end of `handleInitialize` when no pending, and end of whichever review-tool handler clears `pending`. Wrap it in a helper (`applyHooksIfNotPending`) to keep the invariant in one spot.

### 3.4 Fixed review template (inline const in `cmd/mcp.go`)
```
⚠️ ctxloom bundle review required

These bundles changed since last session. Bundle content can contain prompt
injection. Until you respond, bundle-touching tools are blocked.

NEW:
  - {name} @ {newSha[:8]} (from {remote}, {size}B)

MODIFIED:
  - {name} {oldSha[:8]} → {newSha[:8]} (from {remote}, {size}B)

Reply:
  A               Approve all this session
  D               Decline all (keep previous SHAs, skip new installs)
  D <name>        Decline one
  T <remote>      Trust remote going forward (persists to config)
  S <name>        Show bundle verbatim
  R               Review each one at a time
```

Instructions block (added to `initialize.instructions`):
> If a bundle review is pending, the FIRST content of your FIRST response MUST be the review template, verbatim, in a fenced block. Do not paraphrase or summarize. Do not call any other tool. Wait for the user's choice, then call `acknowledge_bundle_review`, `decline_bundle`, `show_bundle_verbatim`, or `trust_remote`. Continue until pending clears.

---

## Phase 4 — Tool gate, prepend middleware, new tools

### 4.1 Gate — top of `handleToolsCall` (`cmd/mcp.go:1324`)
```go
if s.review.hasPending() && !reviewAllowed(params.Name) {
    return blockedToolResult(params.Name, s.review.pending)
}
```

Categorization principle: BLOCK anything that (a) returns bundle bodies to the model, (b) executes bundle-provided code (hooks), (c) replays prior session content that may contain bundle output the user hasn't re-approved, or (d) can re-trigger sync / mutate the pending set. ALLOW everything else.

**ALLOWED during pending review:**

| Category | Tools |
|---|---|
| Review machinery (new) | `acknowledge_bundle_review`, `decline_bundle`, `show_bundle_verbatim`, `trust_remote` |
| Pure metadata listings | `list_remotes`, `list_profiles`, `list_prompts`, `list_fragments`, `list_mcp_servers`, `list_sessions`, `list_bundles` (if present) |
| Metadata get/browse (no bundle bodies) | `get_profile`, `browse_remote`, `browse_session_history`, `search_remotes`, `discover_remotes`, `get_previous_session` |
| Local-only mutations | `create_bundle`, `update_bundle`, `delete_bundle`, `create_fragment`, `delete_fragment`, `create_profile`, `update_profile`, `delete_profile` |
| Config mutations (no fetch) | `add_remote` (registration only, confirmed at `cmd/mcp.go:1165`), `remove_remote`, `add_mcp_server`, `remove_mcp_server`, `set_mcp_auto_register` |
| Outbound | `push_bundle` |
| Session compaction (reduces only) | `compact_session` |

**BLOCKED during pending review:**

| Reason | Tools |
|---|---|
| Returns bundle bodies | `assemble_context`, `get_fragment`, `get_prompt`, `search_content` |
| Replays prior session content (may include bundle output) | `load_session`, `recover_session` |
| Executes bundle-provided code | `apply_hooks` |
| Triggers fetch / re-sync (would re-stack pending) | `update_remote` (fetches, per its description at `cmd/mcp.go:1198`), `pull_remote`, `sync_dependencies` |

**Verify before merging PR 3:** confirm `list_prompts` returns only metadata (names) and not prompt bodies. `cmd/mcp.go:785` schema suggests metadata-only; double-check the handler. If the handler embeds body text, move to blocked.

**Block response shape:** `blockedToolResult` returns an MCP tool result whose content is the rendered review template + a one-line "tool `<name>` is blocked until acknowledged" suffix. No partial data leaks.

### 4.2 Prepend middleware — before result marshalling (`cmd/mcp.go:1454`)
If `s.review.hasPending()`, prepend the rendered template to the tool result content. Keeps the protocol visible on every allowed call until acknowledged.

### 4.3 New tools (register in `getLocalTools`, `cmd/mcp.go:571`)

**`acknowledge_bundle_review(decision: "approve_all")`**
- Merge `lock.pending.yaml` into `lock.yaml`. Clear `s.review.pending`.

**`decline_bundle(name?: string)`**
- No name → drop all pending entries; modified bundles continue reading at active SHA; new bundles stay uninstalled. Clear `s.review.pending`.
- With name → drop just that entry. Clear `pending` if empty.

**`show_bundle_verbatim(name: string)`**
- Construct a temporary `BundleReader` against the *pending* lockfile to read the new SHA via `Fetcher.FetchFile`. Return raw bytes in a fenced block with header `name (sha=<newSha>, from <remote>)`. Does not mutate state.

**`trust_remote(name: string, trust: bool)`**
- Mutate `Remote.TrustBundles`, persist via `registry.save()` (`internal/remote/registry.go:96`).
- If `trust=true`: auto-approve any pending entries from that remote (move into active lockfile, prune from `s.review.pending`). Clear `pending` if empty.

---

## Testing

- **Unit** `bundle_reader_test.go` — reads at active SHA; falls back to fs for local bundles; uses cached factory (no API path).
- **Unit** `bundle_diff_test.go` — added/modified detection; trust-filter exclusion.
- **Unit** `mcp_review_test.go` — gate blocks/allows correct tools (table-driven over the full allow/block lists from 4.1); `acknowledge_bundle_review` merges lockfiles AND applies hooks; `decline_bundle` (named + bulk) preserves prior SHAs AND applies hooks against the old set when `pending` clears; `trust_remote` prunes matching pending entries AND triggers hook application iff `pending` reaches empty.
- **Unit** deferred-hooks invariant — startup with non-empty pending does NOT invoke the hook phase; only the review-tool path that clears pending does.
- **Integration** — extend wire-protocol test from commit `87ee531`:
  - Init with pending → `assemble_context` blocked → `acknowledge_bundle_review` → succeeds with new content.
  - Init with pending → `decline_bundle` → `assemble_context` succeeds with old content.
- **Migration test** — legacy extracted bundle dir → cleanup runs, `BundleReader` serves content from git cache instead.
- **Manual** — fresh sync of new remote with bundle, observe template at startup; modify upstream, sync, decline, verify old content still served; `CTXLOOM_AUTO_APPROVE_BUNDLES=1` bypass logs warning.

---

## PRs (in order)

1. **PR 1 — `BundleReader` + remove extraction.** Phase 1. Structural change but no user-visible behavior shift (reads produce the same bytes via different mechanism). Legacy dir cleanup included.
2. **PR 2 — Pending lockfile + diff.** Phase 2. Lockfile split, `BundleChangeSet`, `TrustBundles` schema field.
3. **PR 3 — Review flow.** Phases 3 + 4. MCP review state, initialize wiring, template, gate, prepend middleware, new tools, env-var bypass. Tests at each layer.

Each PR is independently revertable; behavior change visible to users lands only in PR 3.

---

## Deferred / follow-ups

- In-memory read cache in `BundleReader` if profiling warrants.
- `ctxloom gc` command to prune the git clone cache.
- Promoting repeated session declines into permanent `Reference.ContentVersion` pins in config.
- A "show one, then re-present template with ✓ marker" UX loop for the `R` (review each) flow.

---

## Open items resolved

- **Injection site:** both — `initialize.instructions` + sticky tool-result prepend.
- **Change scope:** new bundles + content-modified bundles (SHA diff). Profile/hook deltas excluded from this iteration.
- **Trust persistence:** per-session approve default; per-remote trust persisted via config flag (`TrustBundles`).
- **Non-interactive bypass:** explicit env var `CTXLOOM_AUTO_APPROVE_BUNDLES=1` with stderr warning. No TTY-magic.
- **Block scope:** B3 (hard block — only allowlisted tools callable until acknowledged).
- **Template authoring:** fixed verbatim template, not natural-language paraphrase.
- **Decline persistence:** per-session only.
- **Decline of NEW bundle:** don't install at all.
- **Storage model:** version-aware getter at repo layer (`BundleReader → cacheFetcher → GitCloneFetcher`). No fs extraction.

## Open items remaining

- Concrete name + signature for the legacy-dir cleanup prompt/env-var pair (revisit in PR 1).
- Exact JSON Schema shape for the new tools' params (write during PR 3 to match conventions from commit `e7a865f`).
