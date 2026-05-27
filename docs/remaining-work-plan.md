# Remaining work after the `feat/bundle-mcp-tools` branch

> **Status:** living plan, written 2026-05-27. Captures items consciously deferred during the long branch so they don't fall on the floor. None are blocking the branch from merging.

The branch landed: bundle review on startup with SHA-keyed reads, native task layer replacing flesler/mcp-tasks, harp-named sessions with pre-launch picker, MCP footprint cut (~46 → 18 tools + 12 resources), embedded tasks bundle, hook command path-rewriting, and a coverage push to ~60% aggregate.

What follows is the un-done set.

---

## Coverage gaps

Current state (filtered, post-`.coverignore`):

| Package | Coverage | Why it's not 100% |
|---|---|---|
| `cmd` | 13% (raw) / ~34% (filtered) | cobra command lifecycle; gains require helper extractions |
| `internal/lm/grpc` | 44% (raw) / ~70% (filtered) | most uncovered is generated protobuf (filtered); a few `NewPluginClient` edge cases remain |
| `internal/lm/backends` | 68% | codex / gemini specific paths haven't been exercised broadly against the existing afero seam |
| `internal/ptyrunner` | 71% | PTY syscalls — genuinely heroic, deferred indefinitely |
| `internal/filelock` | 75% | edge cases on contention; low-value |

### Cheapest remaining wins

1. **`cmd/bundle.go`** — distill chains, push helpers. Extract pure functions from RunE bodies in the pattern of `bindSessionFromPayload` / `resolveResumeIntentWith`. Each command ~30 lines of refactor + ~50 lines of test.
2. **`cmd/profile.go`** — install resolution logic (which default to set, parent profile walking). Same pattern.
3. **`cmd/config.go`** — `config show` and `config get <section>` are tiny; the section-name switch is the testable surface.
4. **`cmd/remote_update.go`** — sync logic has clear decision branches.
5. **`internal/lm/backends`** codex/gemini-specific paths — existing afero seams already there, just write more cases against the fake fs. Could go from 68% → ~80%.

### What stops us going further

- **PTY syscalls** in `ptyrunner` — platform-conditional, requires real terminals or significant fakery
- **End-to-end LLM-driven flows** (`compact_session` against a real plugin) — would need a fake-plugin server fixture
- **`internal/lm/grpc::NewPluginClient`** still has hashicorp/go-plugin internals we don't mock; the cheaply-mockable surface is already covered via `dialPluginConnection`

---

## Functional items deferred from prior plans

From `docs/ctxloom-tasks-plan.md`'s "Deferred / follow-ups":

- **`task_link_session <harp-id> <harp-session-name>`** — explicit task↔session association. Plan-stamping covers most of the use case implicitly; revisit only if needed.
- **Cross-project task search** — `~/.ctxloom/tasks-index.yaml` aggregating per-project task files for `ctxloom tasks list --all-projects`. Out of scope until someone asks.
- **Status customization via env** — flesler exposes `STATUS_WIP`, `STATUSES`, `KEEP_DELETED`, etc. as env vars. We hardcode "In Progress" / "To Do" / "Done" / "Archived". Revisit if user-defined workflows materialize.
- **Transcript symlink in harp dir** — `backends.Session` has no Path field; adding one across all backend implementations for snapshot fidelity isn't worth it. essence.md is the actual resume source.
- **AUTO_WIP enforcement** — capping in-progress count to N. User discipline for now.
- **Reminders section** — flesler has a "Reminders" status the LLM constantly sees. Skip v1; revisit if useful.
- **`harp-go` extraction** — promote `internal/harp/` to its own repo at `github.com/benjaminabbitt/harp-go`. Trigger when (a) a non-ctxloom consumer wants to vendor it, (b) the API stabilizes, or (c) we want versioned releases independent of ctxloom.

---

## Open issues to verify

These are documented assumptions the branch made; each is worth confirming before someone else has to debug it.

- **Bundle review gate ordering.** Built-in bundles (shipped in the binary) bypass the review gate by design — you trust the binary. Remote-pulled bundles still go through. Worth a code audit to confirm the allowlist + SCM-tagging logic matches this contract in every codepath.
- **OSC2 terminal title** on Windows Terminal — we emit `\033]2;ctxloom · <harp>\007` for everyone. xterm, iTerm2, alacritty, WezTerm, kitty render it; Windows Terminal hasn't been smoke-tested.
- **Picker `d<N>` resilience** — shells out via `os.Executable()` resolved at picker time. If the binary moves mid-session, the shell-out fails. Uncommon; documented in the picker comment.
- **Backend session-id binding precedence** — three layers (SessionStart hook → compact-time → transcript-content scan). Each is idempotent. Confirm there's no interleaving where a stale SessionID could be written over a fresh one (the `entry.SessionID != ""` short-circuit should prevent this; worth a re-read).

---

## Pattern catalog for future expansion

Repeatable techniques the coverage push established. Use these for any subsequent gap-closure work:

1. **IoC seam over `exec.Command`** — `var execCommand = exec.Command`. Tests override; production unchanged. Example: `cmd/run.go::shellOutDistill`.
2. **Pure-helper extraction from cobra RunE** — pull decision logic out; cobra wrapper composes flags into a struct and delegates. Example: `cmd/run.go::resolveResumeIntentWith` + `resumeFlags`.
3. **Interface mock for third-party concrete types** — wrap with our own interface, swap in tests. Example: `internal/lm/grpc::pluginConnection` over `*plugin.Client`.
4. **Afero filesystem injection** — already widespread; just exercise more cases against the fake fs.
5. **In-process MCP handler tests** — `&ctxServer{}` + `withProjectDir(t)`, no subprocess. Example: `cmd/mcp_resources_test.go`.
6. **Wire-protocol subprocess tests** — only when SDK serialization or registration is the contract being pinned. Example: `cmd/mcp_resources_integration_test.go`.

Avoid these (genuine heroics):
- Subprocess tests for code that's not protocol-shaped
- PTY/TTY emulation
- End-to-end tests requiring a real LLM

---

## Distribution / external

- **`ctxloom-default-tasks` bundle promotion** — the canonical home for the tasks bundle is `github.com/ctxloom/ctxloom-default`, but it currently ships embedded in the binary (cleaner for distribution). The standalone bundle YAML at `examples/bundles/tasks.yaml` exists for users who want to inspect or fork it. No action required unless we want to make the bundle optional rather than always-on.

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
