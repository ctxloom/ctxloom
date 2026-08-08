# `codex` — `internal/codex`

ctxloom's adapter for the OpenAI Codex CLI. It declares codex's two process
surfaces, resolves and seeds `CODEX_HOME`, writes codex's native
config/prompt/skill/`AGENTS.md` files, and launches `codex` — or `codex-acp` for
structured chat.

Its distinguishing contract is **`CODEX_HOME` ownership**. codex is the only backend
that registers a `SetExecuteEnv` contributor, because its config, prompts and skills
are `CODEX_HOME`-relative rather than cwd-relative, and it has **no out-of-cwd
redirect flags at all**. It also owns a credential gate: `Setup` seeds or verifies
credentials per isolation axis and stores a `credentialErr` that `Execute` returns
first.

On the native-surfaces rule it is clean — the `$CODEX_HOME/sessions/**/rollout-*.jsonl`
scraper was deleted outright and `History()` returns `nil`.

## Exported surface

| Symbol | Location | Meaning |
|---|---|---|
| `Codex` | `backend.go:50` | The `agent.Backend`; embeds `LaunchBackend`. Private state: `resolvedProjectDir`, `resolvedTrustAbsPath`, `credentialErr`, `thinking` |
| `NewCodex` | `backend.go:90` | `InitLaunch(NewBaseLifecycle("codex"), &CodexCommands{}, ctxProvider, nil, &agent.CellDelivery{Build: b.buildSurfaces, RawContext: true, ContextHook: true})`, `SetExecuteEnv(b.cellCodexHomeEnv)`, `SetACPTransport(CodexACPTransport)` |
| `CodexConfig` | `backend.go:29` | Decode target. `BinaryPath`/`Args`/`Env`/`Thinking` live; `Model` decoded and never read |
| `CodexConfig.BackendType` | `backend.go:45` | `"codex"` |
| `Setup` | `backend.go:230` | Resolves `CODEX_HOME`, stashes the trust path, runs the credential gate |
| `Execute` | `backend.go:351` | Credential gate first, then `ExecuteCLI` |
| `buildArgs` | `backend.go:437` | subcommand + model + sandbox tier + approval + positional prompt |
| `Chat` | `chat.go:38` | `agent.StructuredChat` via the generic ACP driver over `codex-acp` |
| `CodexACPTransport` | `chat.go:21` | `{Kind: ACPAdapter, Binary: CodexACPAdapter, InstallCmd: "npm install -g @zed-industries/codex-acp", Publisher: "Zed Industries"}` |
| `EngineCLIs` / `CodexEngineCLIs` | `enginecli.go:167` / `:181` | Surface declarations |
| `GlobalHome` / `ProjectHome` | `commandfiles.go:72` / `:77` | The only export seam for `internal/operations/hooks.go:301,305` |
| `CodexHookWriter` | `settings.go:54` | `agent.SettingsWriter` + `agent.ContextWriter` (`config.toml` + `AGENTS.md`) |
| `NewWriter` | `settings.go:47` | Registry seam (`registry.go:332`) |
| `WriteSettingsWithTrust` | `settings.go:124` | load → strip managed → re-add hooks/MCP/trust → save |
| `WriteSettings` / `RemoveSettings` / `Status` / `WriteContext` / `SettingsPath` | `settings.go:108` / `:205` / `:221` / `:101` / `:68` | Interface methods |
| `MCPRegistrar` | `mcp_registrar.go:22` | Byte-level `[mcp_servers]` merge/remove for `taskloom manage` |
| `Surfaces` / `NewSurfaces` | `surfaces.go:270` / `:311` | codex's `agent.SurfaceSet`; takes `agent.SurfaceInputs` directly (no local copy) |
| `TransformToCodexPrompt` / `codexPromptFile` | `commandfiles.go:108` / `:98` | Prompt-file renderer (live path: `surfaces.go:321`) |

**Stubbed or absent:** `SessionHistory` is `nil` (`backend.go:108`).
`SharedRealization` returns **`nil, false` always** (`surfaces.go:414`) — the
*declaration* that codex has no out-of-cwd redirect. There is no per-tool deny
mechanism at all.

## How it drives the engine

- **Oneshot**: **`codex exec …`** — a distinct subcommand with its own flag set; an unknown flag is a hard exit 2 (`enginecli.go:76-79`, `:185`).
- **Prompt is an argv positional on *both* surfaces** (`enginecli.go:186`, `:197`; `backend.go:470`) — diverging from claude's stdin oneshot.
- **Emitted argv** (`backend.go:437-475`): `[exec] <b.Args…> [--model M] [--sandbox read-only|workspace-write | --dangerously-bypass-approvals-and-sandbox] [--ask-for-approval never] [<prompt>]`.
- **Flag vocabulary** (`enginecli.go:80-85`): `--model`, `--sandbox`, `--dangerously-bypass-approvals-and-sandbox`, `--ask-for-approval` (**interactive only** — `codex exec` rejects it with exit 2, `enginecli.go:206-208`).
- **Env set on the child** (`enginecli.go:162`): `CODEX_HOME`, `CTXLOOM_CONTEXT_FILE`, `CTXLOOM_SESSION_HARP`. The harp is consumed by nothing — codex has no session-naming lever.
- **Structured chat**: `codex-acp` (Zed Industries) on PATH; host-PATH gate then the generic ACP driver (`chat.go:38`). Config overrides pass as `-c key=value` (`chat.go:89`).

## Capabilities

| Capability | Answer |
|---|---|
| Backend id | `"codex"` (`registry.go:322`); binary `codex` |
| Permission tiers | plan **or** `SkipSetup` → `--sandbox read-only` + `--ask-for-approval never`; default/acceptEdits → `--sandbox workspace-write`; bypass → `--dangerously-bypass-approvals-and-sandbox` and **no `--sandbox` at all** (`backend.go:452-467`). **plan and acceptEdits are not distinguished the way claude distinguishes them** — acceptEdits collapses into the default `workspace-write` tier |
| `EnforcesReadOnlyPlan` | **true** (`registry.go:342`), so `plan` is not collapsed. Every posture names its tier explicitly so an upstream default change cannot silently relax it (`enginecli.go:82-84`); bypass deliberately does not reuse `workspace-write` (`backend.go:430-436`) |
| Native per-tool deny list | **no support.** The deny axis is the whole-sandbox tier only. `agent.SurfaceInputs.DenyTools` is accepted by the seam and **silently dropped** — `NewSurfaces` (`surfaces.go:311`) never reads it, with no warning |
| Context surface | **Two additive routes, composed** (`agent.ComposedDelivery`, `surfaces.go:330-334`): (1) `AGENTS.md`, read natively at session start, marker-merged via `WriteContext` (`settings.go:101`; `agentsMDSurface.Deliver`, `surfaces.go:165`) — codex reads `AGENTS.md`, never `CLAUDE.md`; (2) a **hook-mediated cache file** — a per-run content-hash file at `<cwd>/.ctxloom/cache/context/<hash>.md` that codex never opens, plus a `config.toml` `[hooks]` SessionStart command with the hash baked into its argv; codex ingests the **hook's output** (`enginecli.go:131-150`). codex is the one engine that fires the inject-context hook (`CellDelivery{RawContext: true, ContextHook: true}`, `backend.go:109`). **No `--append-system-prompt-file` equivalent exists** (`enginecli.go:24-27`) — which is why per-agent isolation for codex needs a private cwd or a container |
| MCP | `[mcp_servers]` tables in `config.toml` (`addMCPServers`, `settings.go:433`; `mcpServerToTOMLEntry`, `settings.go:470`; `configSurface.Deliver`, `surfaces.go:224`). Because hooks and MCP share one file, `ProbesFor(ProbeKindMCP)` is legitimately empty. Per-agent scoping comes from the per-run `CODEX_HOME` (`cellCodexHomeEnv`, `backend.go:322`) |
| Commands | **`$CODEX_HOME/prompts/<name>.md` — global only** (`commandfiles.go:98`, `:108`; delivered at `surfaces.go:316-323`, labelled `"codex/commands (global $CODEX_HOME)"`) |
| Skills | **`$CODEX_HOME/skills/<name>/SKILL.md` — global only** (`writeCodexSkillPackages`, `skillfiles.go:60`; `surfaces.go:324-331`) |
| | **codex has no project-level prompts or skills dir** (`enginecli.go:60-66`), so an isolated *directory* alone would not isolate them; only the per-run `CODEX_HOME` does |
| One-shot / resume | **Supported.** In both `resumeCapableBackends` and `oneShotSupportedBackends` (`spawner.go:226`, `:249`). Resume-id capture is in `internal/acp`. codex has no session-naming flag |
| Transcript | **No scrape.** `SessionHistory` is `nil`; the `rollout-*.jsonl` reader was deleted (`capabilities.go:17-25`) after its envelope-vs-flat parsing mismatch silently returned zero-entry sessions. Opt-in vendor reader at `internal/operations/vendorreader.go:72` — the reference implementation the other readers copy |
| Model + auth | `--model` emitted only when non-empty; empty lets codex resolve its account-scoped default. Auth: `OPENAI_API_KEY` (live-verified trigger; **`CODEX_API_KEY` is not a confirmed trigger and is deliberately excluded**, `internal/lm/isolation/auth.go:186-196`), else the ChatGPT subscription `auth.json`. The credential gate `ensureCodexCredentials` (`backend.go:272`) picks one of three treatments by `codexHomeSource` (`backend.go:189`). Reasoning rides `-c model_reasoning_effort=…` with `high`→`xhigh` (`chat.go:112`) |
| Isolation | **Supported; container auth works.** Scoped env passthrough plus a **read-only** mount of `~/.codex/auth.json` — RO is safe because non-interactive codex never refreshes the token in place, unlike claude (`internal/lm/isolation/auth.go:198-245`). ctxloom also pre-seeds codex's own trust prompt by writing `[projects."<abs>"] trust_level` (`addProjectTrust`, `settings.go:161`) |
| Status | **Experimental**, with a registry `LIVE-UNTESTED` banner: "codex has never been run against a real account on any dev host" (`registry.go:319-321`). Every format claim — the `[hooks]` array-of-tables shape, sandbox flag names, prompt frontmatter keys — derives from documentation plus hermetic tests, never an authenticated run (`settings.go:6-30`) |

## Invariants

1. **`Setup` must precede `Execute`** for `credentialErr` and `resolvedProjectDir` to be meaningful. `cellCodexHomeEnv` has a defensive re-derivation (`backend.go:327-329`); `Execute` has **no** guard, so a Setup-less path silently skips the credential gate (benign today, since `SkipSetup` keeps codex's already-authenticated global home).
2. **`CODEX_HOME` *is* the `.codex` dir, not its parent** (`enginecli.go:53-56`). `cellScopedCodexHome(dir)` = `<dir>/.codex` is the single naming of that rule (`surfaces.go:81`).
3. **`configSurface.Deliver`'s `dir` argument is ignored whenever `homeOverride != ""`** — which is always, on the live launch path (`surfaces.go:216-223`).
4. **`trustAbsPath` is meaningful only alongside `homeOverride`** (`surfaces.go:205-207`) — documented, not enforced.
5. **Ownership marker is `agent.IsManaged(cmd, "ctxloom")`** for both hooks and MCP servers (`settings.go:252`).
6. **`mcpServerToTOMLEntry` is the single source of the MCP entry shape**, shared by both writers of the file.
7. **`Surfaces.SharedRealization` returning `nil, false` is a load-bearing declaration** depended on by `deliverOneShared`.
8. **Context correctness depends on the composition** `routes = [contextSurface, agentsMDSurface]` (`surfaces.go:326-334`); `contextSurface` alone silently delivers nothing on the materialize path.
9. **An empty prompt emits no positional** (`backend.go:470`), so a oneshot becomes `codex exec --sandbox …` with no task. Upstream guards this; it is pinned as expected behavior (`backend_test.go:187`).

## Divergences from documented or implied behavior

- **On every plain host run, ctxloom copies the user's OpenAI credentials into the project working tree**: `seedCodexHomeFn(req.WorkDir)` (`backend.go:287`) → `<WorkDir>/.codex/auth.json` (`internal/lm/isolation/auth.go:685-702`, `:875-887`). `internal/gitignore/gitignore.go:42` covers `.codex/config.toml` but **not** `.codex/auth.json`, and no `Cleanup` path removes it.
- **`save` writes a 0-byte `config.toml` when the table is empty** — `toml.Encode(map[string]any{})` produces zero bytes on the pinned go-toml v2.4.3, and `agent.AtomicWriteFile` has no zero-length guard, so prior contents are destroyed with exit 0 (`settings.go:195-201`). Reachable from `RemoveSettings` (`settings.go:217`) — the `configSurface` Cleanup path on **every isolated run**.
- **A TOML parse failure is converted to an empty table and written back, replacing every user key** (`settings.go:187-190`). The same package's `mcpTOMLDoc` (`mcp_registrar.go:101`) treats the identical failure as a hard error — **the two readers of the same file disagree**. The warning does not name the `.ctxloom.bak` backup, so there is no recovery pointer.
- **The credential gate reads the ambient process `OPENAI_API_KEY`** (`backend.go:276`) while its caller resolves `CODEX_HOME` from `req.Env`, so a per-agent `env:` key that would have authenticated the child is invisible and `Execute` refuses a run that would have worked.
- **A configured unified `SessionEnd` hook is silently discarded for codex** — no route, no warning (`settings.go:319-329`). Every sibling routes it (`internal/claude/claude.go:590`, `internal/antigravity/antigravity.go:399`, `internal/kiro/settings.go:142`).
- **An explicitly-set `CODEX_HOME` not ending in `/.codex` is silently rewritten** (`backend.go:163-167`): `CODEX_HOME=/opt/codexhome` means the child receives `/opt/codexhome/.codex`, with no diagnostic. Pinned as expected at `backend_test.go:282`.
- **The declared "SINGLE source of the names codex reads" is contradicted by literals in three files** (`enginecli.go:45-49` vs `mcp_registrar.go:49,51`; `commandfiles.go:23,30`; `skillfiles.go:35`) — including consumers the comment explicitly names.
- **`agent.SurfaceInputs.DenyTools` configured for a codex agent is accepted and silently dropped** (`internal/shared/agent/cells.go:179-181` vs `surfaces.go:311`).
- **The field doc for the trust pre-seed says it happens "ONLY when resolveCodexProjectDir found an isolation-provided CODEX_HOME"**, but the code (`if source != codexHomeInTree`) also admits the container-fresh axis (`backend.go:59-63` vs `:234`). Separately, ctxloom auto-answering codex's own trust prompt is a trust-boundary decision documented only in a code comment, not in `docs/trust-model.md`.
- **Two writers disagree about emptiness**: `Uninstall` leaves an empty `[mcp_servers]` table where `removeManagedMCP` deletes the key (`mcp_registrar.go:71-80` vs `settings.go:424-426`); and a whitespace-only `config.toml` round-trips to 0 bytes, which `cmd/taskloom/manage.go:141` writes unconditionally.
- **`Configure` silently ignores a wrong-typed config** — no `else`, no warning (`backend.go:335`).
- **`afero.Exists` errors are discarded** at `settings.go:208`, `:225`, so an unreadable config is indistinguishable from an absent one.
- **`Surfaces.Context` and `Surfaces.AgentsMD` are exported**, letting a caller bypass the `routes` composition that exists precisely to stop the `AGENTS.md` write from being dropped (`surfaces.go:271-272`). `contextSurface.Deliver` returns `nil, nil` on empty fragments — correct only *as composed*.
- **`CodexCommands` / `RegisterFromContent` / `WriteCommandFiles` / `promptsDirFor` / `codexPromptsDir` form an unreachable chain**; `WriteSkillFiles`, `skillsDirFor` and `codexSkillsDir` are **test-only** — production goes straight to `writeCodexSkillPackages` (`surfaces.go:328`).
- **Unverified**: whether `req.WorkDir` in a container run is the in-container path (a host path would make the pre-seeded `[projects."<path>"]` trust entry name a directory codex never sees), and whether the SessionStart hook's baked cache-file path is cwd-relative or absolute. On the isolation-provided axis, `config.toml` lands in the ephemeral home while the cache file lands in the cell dir, so an absolute path baked from the wrong root would deliver zero context silently.

## See also

[Capability matrix](capability-matrix.md) · [Backend abstraction](backend-abstraction.md) · [Plugin wire](grpc-wire.md) · [Isolation](isolation.md)
