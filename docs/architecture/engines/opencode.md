# `opencode` — `internal/opencode`

ctxloom's adapter for the `opencode` CLI. It delivers ctxloom's managed
configuration — model, MCP servers, assembled context, read-only permission, custom
commands, Agent Skill packages — into opencode's **own native surfaces**, drives a
structured or oneshot turn over first-party `opencode acp`, launches opencode's TUI,
and reads transcripts back via `opencode session list` / `opencode export`.

Its central invariant is the **transient overlay**: the project-local
`opencode.json` is snapshotted, ctxloom's managed keys are merged in, the run
happens, then the file is restored byte-exact — so a plan run never leaves a
`permission: {edit:deny, bash:deny}` block behind.

It is **the best citizen of the fleet on the native-surfaces rule**: no scraping of a
private format anywhere in the unit, with an explicit written refusal to touch
`~/.local/share/opencode/opencode.db` (`capabilities.go:19-26`). Every key it writes
was verified against `opencode debug config` on opencode 1.18.1. It is also the only
engine with a **live `History()`**.

External surface is exactly four symbols, all consumed by
`internal/lm/backends/registry.go:425-438`: `NewOpencode`, `OpencodeConfig`,
`NewWriter`, `NewSurfaces`.

## Exported surface

| Symbol | Location | Meaning |
|---|---|---|
| `OpencodeConfig` | `backend.go:36` | mapstructure body: `Model`, `BinaryPath`, `Args`, `Env`, `Thinking` |
| `OpencodeConfig.BackendType` | `backend.go:50` | `"opencode"` |
| `Opencode` | `backend.go:53` | The backend struct; embeds `agent.LaunchBackend`; fields `model`, `pendingCommands`, `pendingContext`, `pendingSkills` |
| `NewOpencode` | `backend.go:75` | Constructor; wires `InitLaunch` with the state-stashing `CellDelivery.Build` closure (`backend.go:95-100`) |
| `Configure` | `backend.go:106` | Applies CLI config; warns on `thinking`; **silently returns on a wrong config type** (`:108-110`) |
| `SupportedModes` | `backend.go:126` | `{ModeInteractive, ModeOneshot}` |
| `Execute` | `backend.go:134` | dry-run → interactive branch → else a one-message ACP turn drained to stdout |
| `Chat` | `chat.go:41` | `agent.StructuredChat` (compile-time assert `chat.go:15`) — overlay + `acp.ChatDriver` + LIFO revert |
| `launchInteractive` | `interactive.go:38` | TUI launch with the same overlay/revert dance |
| `buildInteractiveArgs` | `interactive.go:136` | The **only** argv builder |
| `chatACPConfig` | `chat.go:148` | `acp.ACPConfig{Command: BinaryPath + " acp", Args, Env}` |
| `opencodeSessionHistory` | `capabilities.go:41` | `agent.SessionHistory` over opencode's own CLI |
| `newOpencodeSessionHistory` / `WithOpencodeSessionRunner` | `capabilities.go:60` / `:54` | Constructor + test-only run seam |
| `managedConfig` | `settings.go:88` | Value object for one merge: `model`, `mcpServers`, `readOnly`, `instructions`, `skillPaths` |
| `applyManaged` | `settings.go:112` | The five-key merge into the parsed `opencode.json` map |
| `writeOpencodeConfig` | `settings.go:262` | **The single read-modify-write entry point** |
| `loadOpencodeConfig` / `saveOpencodeConfig` | `settings.go:232` / `:248` | Fail-loud parse; deterministic-key-order atomic write |
| `snapshotOpencodeConfig` | `settings.go:279` | Bytes-or-absence capture → exact-restore closure |
| `stripManagedMCP` / `stripManagedInstructions` / `stripManagedSkillPath` | `settings.go:302` / `:327` / `:358` | Ledger-scoped removal of ctxloom's entries only |
| `NewWriter` / `OpencodeWriter` | `settings.go:465` / `:479` | `agent.SettingsWriter` + `agent.ContextWriter` for the static materialize path |
| `WriteSettings` / `WriteContext` / `RemoveSettings` / `removeMCP` / `Status` | `settings.go:503` / `:526` / `:597` / `:577` / `:621` | MCP / instructions / context / ledger lifecycle |
| `WriteCommandFiles` / `renderCommandFile` | `commandfiles.go:33` / `:46` | Writes `.opencode/command/<name>.md` front-matter + body |
| `WriteSkillFiles` / `reconcileSkillsSurface` | `skillfiles.go:34` / `:61` | Writes `.opencode/skill/<name>/SKILL.md` **and** reconciles `skills.paths` |
| `NewSurfaces` / `Surfaces` | `surfaces.go:122` / `:108` | `agent.SurfaceSet` over `{Context, Config, Commands, Skills}` |
| `Surfaces.SharedRealization` | `surfaces.go:183` | `nil, false` — opencode has no out-of-cwd redirect |

**Honest refusals** (rather than stubs): `GetSessionByPath` (`capabilities.go:177`)
always errors *"opencode has no file-backed transcript"*; `TranscriptPathFromHook`
(`capabilities.go:183`) returns `""`. `opencodeCommands.RegisterFromContent`
(`capabilities.go:32`) returns `nil` and is dead.

## How it drives the engine

Two paths, both via the binary — never an SDK or API.

- **Structured chat and oneshot**: the generic ACP driver over **first-party `opencode acp`** — `acp.ACPConfig{Command: b.BinaryPath + " acp", Args: b.Args, Env: b.Env}` (`chat.go:148-155`), transport `ACPNative` (`registry.go:224`). **No `--model`, no `--agent`/`--agent-engine`, no reasoning flags — `opencode acp` rejects them** (`chat.go:140-147`). Oneshot is a one-message projection of `Chat` (`backend.go:134-197`).
- **Interactive TUI**: the TUI is opencode's **default subcommand, so no subcommand token is emitted** — unlike codex `exec` or kiro `chat` (`interactive.go:128-135`). The complete argv vocabulary (`interactive.go:136-152`) is: a copy of `b.Args`, plus `--auto` iff `PermissionBypass && !SkipSetup`, plus `--prompt <text>` iff a prompt is present.
- **History**: `opencode session list --format json` (`capabilities.go:116`) and `opencode export <id>` (`capabilities.go:162`).

## Capabilities

| Capability | Answer |
|---|---|
| Backend id | `"opencode"` (`registry.go:423`); binary `opencode` |
| Permission tiers | **plan → an `opencode.json` `permission` block `{edit:"deny", bash:"deny"}`** (`readOnlyPermission`, `settings.go:57-59`; applied `:157-174`). **bypass → argv `--auto`** (`interactive.go:141-145`), and never also the deny block. acceptEdits and default → no managed permission key. `SkipSetup` also forces `readOnly = true` (`interactive.go:166-168`) |
| `EnforcesReadOnlyPlan` | **true** (`registry.go:437`), so `plan` is preserved. Rationale (`settings.go:48-56`): `edit` gates **both** opencode's edit and write tools (there is no separate `write` key) and `bash` is denied too — **stricter than opencode's own built-in `plan` agent, which leaves bash allowed.** That gap is exactly why ctxloom writes its own permission rather than launching `--agent plan` |
| Native per-tool deny list | **no general support.** The only deny mechanism is the fixed two-key `readOnlyPermission` map (`edit` + `bash`); there is no plumbing from a ctxloom-side per-tool deny list. Denial granularity is coarse by design |
| Context surface | Assembled context is written to the ctxloom-owned `.opencode/ctxloom-context.md` (`opencodeContextFile`, `settings.go:36`) and referenced from the `instructions[]` key of `opencode.json` — opencode's documented "additional instruction files" mechanism. Live path `materializeContextSurface` (`chat.go:190-205`), reverted after the run; static path `WriteContext` (`settings.go:526`) |
| MCP | **Yes**, via opencode's own `mcp` key in the project-local `opencode.json` — **not over the ACP wire**; opencode never receives `mcpServers` on `session/new` (`chat.go:28-33`). Two schema shapes: `opencodeMCPLocal` (`{type, command[], environment, enabled}`, `settings.go:66`) and `opencodeMCPRemote` (`{type:"remote", url, headers, enabled}`, `settings.go:79` — one remote shape covers both HTTP and SSE). Composition order at `composeManagedServers` (`settings.go:440`). Ownership is tracked in an **out-of-file sidecar ledger** `.ctxloom-opencode-managed` (`settings.go:42`), because opencode's mcp entries are `additionalProperties:false` and no in-file marker is possible |
| Commands | `.opencode/command/<name>.md` (`opencodeCommandDir`, `commandfiles.go:19`), manifest `.ctxloom-manifest` (`:26`), rendered as front-matter + body by `renderCommandFile` (`:46`) |
| Skills | `.opencode/skill/<name>/SKILL.md` (`opencodeSkillDir`, `skillfiles.go:22`), manifest `.ctxloom-skills-manifest` (`:27`), plus belt-and-suspenders registration in `skills.paths`. That registration is **not load-bearing** — `settings.go:94-102` documents that the default dir is already auto-discovered with no config entry at all, verified on 1.18.1 |
| One-shot / resume | Oneshot mode **is** supported (`SupportedModes`, `backend.go:126`) as a one-message ACP turn. **Resume: no opencode-specific path** — this package contains no resume code, and opencode is absent from `resumeCapableBackends` (`spawner.go:225-228`), so `driving: oneshot` **fails loud** at the resume-capability gate before the release gate. Generic ACP resume, when it applies, is gated on the agent advertising `loadSession` (`internal/acp/session.go:373-376`) |
| Transcript | **Native export command, no scrape** — `opencode session list --format json` filtered to the workdir and sorted by updated-desc (`capabilities.go:116-160`), and `opencode export <id>` parsed by `parseOpencodeExport` (`capabilities.go:233`). The only engine with a live `History()` |
| Model + auth | The model rides `opencode.json`'s `model` key — `opencode acp` has no `--model` flag (`chat.go:139-141`; applied `settings.go:113-120`). The provider is decided by opencode's own resolution of the model string; `ExecuteResult.ModelInfo.Provider` is hard-set to `"opencode"` and is called honest, not a placeholder (`backend.go:136-138`). `thinking` is a **documented no-op** — warning only. Auth is ambient to the binary: env passthrough (`OPENROUTER_API_KEY` is the trigger — OpenRouter is ctxloom's documented default opencode provider) or the file `opencode auth login` writes |
| Isolation | **Supported.** A composable engine fragment installed via the official `https://opencode.ai/install` script (`internal/lm/isolation/profile.go:300-317`, selected at `:462-467`). Container auth prefers env passthrough, else a **read-only** mount of `~/.local/share/opencode/auth.json` (`auth.go:287-305`). opencode **honours `XDG_DATA_HOME` for credentials (`HonoursVarForCreds: true`, live-verified on 1.18.1) — the opposite of kiro** — so it is registered in `credentialSeedSpecs` rather than `curatedHomeSpecs`. **No subscription-vs-container blocker.** Known gap: it is unverified whether a containerized run refreshes `auth.json` in place, which a read-only mount would break (`auth.go:269-274`) |
| Status | **Undeclared.** Not flagged experimental in code or docs — but also not covered by the engine-status caution at all: `opencode` appears nowhere in `website/src/content/docs/concepts/architecture.md` or `reference/environment.md` |

## Invariants

1. **Exactly ONE writer of `opencode.json`** — `writeOpencodeConfig` (`settings.go:262`); the live and static paths share the same merge core.
2. **Transient overlay, no debris**: snapshot → write → run → LIFO revert. The user's file is left byte-exact, and a plan run never leaks its deny block (`settings.go:274-278`, `chat.go:36-39`).
3. **Foreign keys are preserved; a malformed config fails LOUD rather than being clobbered** (`applyManaged`, `settings.go:112`; `loadOpencodeConfig`, `:232`).
4. **opencode's schema is closed / strictly validated** — every key written is schema-known, verified against `opencode.ai/config.json` plus `opencode debug config` on 1.18.1; ownership is therefore tracked out-of-file (`settings.go:24-31`).
5. **Managed-only removal**: the strip functions remove exactly ctxloom's ledgered entries, never a user's (`settings.go:573-576`).
6. **The two skills paths cannot diverge** — `reconcileSkillsSurface` writes the tree AND reconciles `skills.paths` in one operation (`skillfiles.go:61`).
7. **`Setup` must run before `Chat` / `launchInteractive`** — unenforced connascence of execution order.
8. **`chatManaged` mirrors `interactiveManaged`** so the two launch paths cannot diverge on managed content (`chat.go:166-168`).
9. **Deterministic key ordering on save** (`settings.go:248`) so diffs are stable.

## Divergences from documented or implied behavior

- **The oneshot exit code is unconditional.** `Execute` returns `ExitCode: 0` regardless; a turn with zero assistant entries writes zero bytes and reports success (`backend.go:172`, `:180`, `:194`, `:197`). `wroteText` only gates the trailing newline.
- **`parseOpencodeExport` returns an empty transcript with a `nil` error on any shape drift** (`capabilities.go:233-251`), so `/recover` shows empty scrollback and reports success. It hand-decodes the shape (`info`/`messages`/`parts`/`state`, part-type literals `"text"`/`"reasoning"`/`"tool"`/`"step-start"`) with ignore-unknown-fields semantics. The file's own header names this failure mode as what killed the kiro reader.
- **Context, commands and skills silently vanish if `Setup` did not run.** The `CellDelivery.Build` closure is declared a pure mapper but is used only for side effects (`backend.go:95-100`), and the readers (`chat.go:59-60`, `interactive.go:60`) have no zero-check. This is the same shape as the already-fixed `sick-dairy` bug, where `Chat` carried model/mcp/permission but not context, so ACP runs saw zero project context and succeeded anyway.
- **`stripManagedMCP` silently strips nothing on a malformed `mcp`, and the caller clears the ledger anyway** (`settings.go:308-310`; ledger cleared at `:590`, `:617`), orphaning MCP servers permanently while reporting success. `applyManaged` fails **loudly** on the identical condition (`:125`) — the two halves of the same file disagree.
- **`Opencode.Execute` is a near-verbatim ~50-line copy of `ACP.Execute`** (`backend.go:149-198` vs `internal/acp/execute.go:38-94`), including the identical unconditional-exit-code behavior.
- **The overlay lifecycle is written twice** (~45 lines each), differing only in `SkipSetup` gating and `close(out)` (`chat.go:48-131` vs `interactive.go:50-126`).
- **Revert errors are `_ =` discarded on all eight error paths** (`chat.go:61-64`, `:68-70`, `:82-85`, `:99-103`; `interactive.go:69-70`, `:75-77`, `:86-88`, `:99-102`), so a failed restore silently leaves the user's project locked with ctxloom's `{edit:deny, bash:deny}` block — exactly what the snapshot exists to prevent.
- **`SettingsStatus.HooksPresent` is overloaded to mean "the instructions reference exists"** (`settings.go:646-651`); opencode has no hooks at all (`:475-477`), so callers reading the field learn something other than its name.
- **`ListSessions` filters by exact `filepath.Abs` string equality** with no symlink resolution and no ancestor handling (`capabilities.go:134-145`), so a symlinked or subdirectory workDir yields "no history" with a `nil` error — even though opencode resolves projects from the **git root**, as the same file documents at `:69-74`.
- **Whitespace-only context passes the `== ""` guards** (`chat.go:192-194`; `settings.go:531`), so a 1–4 byte context file is written, `instructions[]` points at an empty instruction file, and the run reports full context delivery.
- **`readLedger` maps any read error to `nil`** (`settings.go:656-660`), making permission-denied indistinguishable from absent; the caller then strips nothing and reports success.
- **`Configure` silently discards a config it cannot type-assert** (`backend.go:108-110`).
- **The docs call opencode a "host-only chat spine"** (`website/…/guides/configuration.md:158`, and the `registry.go:406` comment) while opencode **is** a composable container engine with its own install fragment and credential mounts (`internal/lm/isolation/profile.go:300-317`, `:462-467`; `auth.go:255-305`).
- **opencode's artifacts are absent from `WorktreeArtifactPatterns`** (`internal/gitignore/gitignore.go:72-81`), which is documented as "the FULL written set across all engines" — so a per-agent worktree run with `engine: opencode` leaves untracked files, teardown reads the worktree dirty, and the worktree is orphaned. The repo's own `.gitignore:123-127` does list them.
- Dead or unread: `opencodeListEntry.ProjectID` is never read (`capabilities.go:103`); `writeModelConfig` is test-only (`chat.go:161`); `Surfaces.Deliveries()`, `dispatch`, and `opencodeApproaches` are three uncoordinated listings of the same four surfaces (`surfaces.go:137-142`, `:149`, `:156`).

## See also

[Capability matrix](capability-matrix.md) · [Backend abstraction](backend-abstraction.md) · [Plugin wire](grpc-wire.md) · [Isolation](isolation.md)
