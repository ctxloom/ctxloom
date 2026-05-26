# ctxloom-tasks — replacement for mcp-tasks (shipped)

> **Status:** shipped on `feat/bundle-mcp-tools` (2026-05-26). All four phases landed end-to-end. This doc is now a "what shipped + where to find it" map rather than a forward plan. For the design rationale behind each decision, see the commit messages.

## What ships

A ctxloom-native task layer replacing the `flesler/mcp-tasks` dependency, with the harp-named session machinery to track and resume work across runs.

End-to-end flow once the bundle is active:

1. `ctxloom run` mints a harp name (e.g. `swift-amber-falcon`), shows the resume picker if prior sessions exist for the cwd, exports the harp via `CTXLOOM_SESSION_HARP`, and emits the OSC2 escape so the terminal title carries it.
2. The launched LLM sees its own name in `ServerOptions.Instructions`.
3. Claude's `TodoWrite` calls are intercepted by a PostTool hook (`ctxloom tasks capture --stdin`) that mirror-snapshots the current todo list into `.ctxloom/tasks.md`, keyed by harp IDs.
4. Edits to plan-shaped markdown files (`CURRENT_PLAN.md`, `*-plan.md`, `docs/*-plan.md`) trigger a PostFileEdit hook (`ctxloom tasks stamp-plan`) that stamps the active harp into the file's YAML frontmatter `sessions:` list.
5. `compact_session` distills the transcript, writes `~/.ctxloom/sessions/<harp>/{essence,tasks,plans}.md`, forward-binds the backend `session.ID` into the index, and stamps a one-line summary into both the essence frontmatter and the index entry.
6. The next `ctxloom run` shows that summary in the picker; `load_session` can pull the essence back into a fresh session.

## Where each piece lives

| Concern | Files |
|---|---|
| Harp ID generation | `internal/harp/` (native Go port of harp-core; extraction-ready) |
| Task storage + reconciliation | `internal/tasks/{store,reconcile,stamp}.go` |
| Session index | `internal/sessions/index.go` (user-global `~/.ctxloom/sessions/index.yaml`) |
| Resume picker | `internal/sessions/picker.go` (line-based, no TUI) |
| Tasks MCP tools | `cmd/mcp_tools_tasks.go` (`task_list`, `task_add`, `task_set_status`) |
| Tasks CLI | `cmd/tasks_cmd.go` (`ctxloom tasks list/add/status/summary` + hidden `capture`/`stamp-plan`) |
| Session CLI | `cmd/session_cmd.go` (`ctxloom session list/show/rename/forget/distill`) |
| MCP resources | `cmd/mcp_resources.go` (12 resources, 4 templates) |
| Bundle review gate | `cmd/bundle_review*.go` + middleware (from PR `f1262a4`) |
| Bundle hook schema | `internal/bundles/bundles.go::BundleHooks` |
| Builtin bundle resolution | `internal/config/config.go::ResolveBundleHooks` + `resolveBuiltinBundleHooks` |
| Embedded tasks bundle | `resources/builtin_bundles/tasks.yaml` (go:embed; ships in the binary) |
| Compactor + harp-dir layout + frontmatter parsing | `internal/memory/compactor.go` |
| Forward-bind paths | `internal/memory/compactor.go` (compact-time) + `cmd/session_bind.go` (first-tool-call middleware) |
| Hud / window title | `cmd/meta_hud.go` (statusline) + `cmd/run.go` (OSC2) |
| Apply-hooks dedup | `internal/lm/backends/hooks.go::isCtxloomManagedHook` (broad recognition) |

## Architecture decisions

- **Harp impl**: fully native Go inline at `internal/harp/`; extraction to `benjaminabbitt/harp-go` later (mechanical: `git filter-repo` + go.mod rename). Not the existing WASM binding.
- **Task storage**: single-file flesler-style markdown at `.ctxloom/tasks.md`. Sectioned by status; harp ID inline so the LLM echoes it back through TodoWrite.
- **Reconciliation**: harp-id-in-text primary, `sha256(text)[:12]` fallback. Mirror-snapshot — items absent from a TodoWrite move to Archived, not deleted.
- **Auto-capture channel**: `PostToolUse(TodoWrite)` hook shipped by the embedded `tasks` bundle (resources/builtin_bundles/tasks.yaml).
- **Session naming**: harp minted by `ctxloom run` pre-LLM-launch. `session.ID` bound forward via four layered paths, primary first:
  1. **SessionStart hook** (`ctxloom session bind --stdin`, shipped by the embedded tasks bundle). Fires once at session creation with `session_id` + `transcript_path` in the JSON payload — the direct, designed-for-this path. Idempotent.
  2. **First-tool-call middleware** (`cmd/session_bind.go`) safety net for cases where the SessionStart hook didn't fire (version mismatch, hook config drift, alternate backend).
  3. **Compact-time bind** in the compactor for sessions that somehow reached compact without binding earlier.
  4. **Time-window discovery** in `ctxloom session distill <harp>` (`selectClosestSession`) — last-resort rescue for sessions that ended without any of the above firing. Matches against `backend.History().ListSessions()` using the harp's `started_at` (always recorded) and `ended_at` (recorded by `defer` on `ctxloom run` shutdown), within a 5-minute window, end-time tie-broken. Found ID is persisted back to the index.
- **No-harp sessions** (no harp at all — pre-this-release, or sessions spawned outside `ctxloom run`): out of scope. They stay un-named.
- **Resume UI**: line-based numbered picker, not a TUI. Per-row `[s]`/`[t]` checkboxes via `s<N>`/`t<N>`. `d<N>` shells out to `ctxloom session distill <harp>`. Default horizon: min(10, last 7 days). `m` reveals more.
- **One-line summary**: produced as part of the long-essence distillation in a single LLM call (compactor prompt requires it as YAML frontmatter). Picker reads only the frontmatter; task-snapshot summaries are derived deterministically.
- **MCP footprint**: ~46 tools naïve → 18 tools shipped. Listings became resources (`ctxloom://fragments`, `…/profiles`, `…/prompts`, `…/remotes`, `…/mcp-servers`, `…/sessions`, `…/sessions/recent`, `…/tasks/summary`, `…/help`, plus templated `…/{name}` for fragments/profiles/prompts and `…/remotes/{name}/contents`). Writes moved to existing cobra CLI commands.
- **Bundles ship hooks declaratively**: `BundleHooks` field on the schema; built-in bundles + remote bundles both contribute hooks via `ResolveBundleHooks` with SCM markers like `bundle:builtin:tasks` or `bundle:alice/security`.
- **Status visibility**: harp name surfaces in three places — LLM Instructions, `meta hud` statusline, OSC2 terminal title.

## Surviving MCP tools (18)

Review (4): `acknowledge_bundle_review`, `decline_bundle`, `show_bundle_verbatim`, `trust_remote`
Bundle authoring (3): `create_bundle`, `update_bundle`, `delete_bundle`
Tasks (3): `task_list`, `task_add`, `task_set_status`
Sessions (4): `load_session`, `recover_session`, `compact_session`, `get_previous_session`
Context (4): `assemble_context`, `search_content`, `apply_hooks`, `sync_dependencies`

## Commit map (newest first; f1262a4 is the baseline)

```
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

## Deferred (not blocking; revisit if asked)

- **`task_link_session`** — explicit task↔session cross-reference tool. Plan-file stamping covers most of the use case implicitly.
- **Cross-project task search** — aggregate `.ctxloom/tasks.md` across projects via a user-global index. Skip until someone asks.
- **Status customization** — flesler exposes status names via env (`STATUS_WIP`, etc.). We hardcode "In Progress"/"To Do"/"Done"/"Archived". Revisit if user-defined workflows show up.
- **Transcript symlink in harp dir** — Session backend interface doesn't expose a path; adding one for snapshot fidelity isn't worth it. essence.md is the real resume source.
- **AUTO_WIP enforcement** — capping in-progress to N. User discipline for now.
- **`harp-go` extraction** — promote `internal/harp/` to its own repo when API stabilizes or a non-ctxloom consumer asks.

## Open issues to remember

- **Bundle review gate ordering**: built-in bundles ship hooks too — those bypass the review gate by design (you trust the binary). Remote-pulled bundles still go through it. Worth confirming the gate's allowlist logic matches this in a future audit.
- **OSC2 terminal title** is opportunistic — terminals that don't render it silently ignore. Windows Terminal compatibility not yet smoke-tested.
- **Picker `d<N>`** shells out to `ctxloom session distill` via `os.Executable()`. If the binary moves mid-session the call fails; uncommon but documented.
