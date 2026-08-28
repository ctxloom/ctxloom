# ctxloom Glossary

Canonical vocabulary for ctxloom's launch/execution architecture. Use these
terms in code, comments, docs, and plans. Where the industry has a standard we
comply with it; where it doesn't, we coin a collision-free term and say so.

## The launch pipeline

```
control-plane  ──wire──►  runner  ──drives──►  engine ── (provider, model)
  (user config,          (materializes,        (claude-code / codex /
   isolation,             launches)             gemini-cli / direct API)
   assembly)
```

*The **control-plane** assembles a user's configuration into a **loadout** (the
**context**, **MCP**, **hooks**, **commands**, **skills**, and **settings** surfaces) for a
**session** and, over the **wire**, hands it to a **runner**, which composes its
isolation/containerization objects, **injects each surface** into the environment,
and drives the **engine** (whose own **engine agents** we merely pass through).*

## Terms

| term | meaning | maps to today |
|---|---|---|
| **control-plane** | Everything before the wire: user-facing configuration & setup, profile/bundle/context assembly, isolation policy, and spawning the runner into its worktree/container. Owns what the user configures; transmits it. | `internal/cli/run.go`, `internal/config`, `internal/lm/isolation`, `internal/lm/backends` (assembly) |
| **wire** | The network-agnostic control-plane→runner transport. Carries **all** data the runner needs; assumes **no** shared filesystem (the runner may be remote). | gRPC (`SetupRequest`, plugin server) |
| **virtualized-process-io (vpio)** | The host-side transport for an interactive agent **turn**, formalized behind one interface so the frontend (raw-terminal ownership, SIGWINCH→resize plumbing, the termui surround, stdin-close semantics, exit propagation) never touches a transport directly. Distinct from the **wire**: the wire carries the *loadout*, once, before the turn starts; vpio carries the *turn itself* (stdio + resize + signal + exit), for as long as it runs. Current (only) implementation: **go-plugin** — wraps the existing hashicorp/go-plugin-backed bidirectional `Run` RPC (`internal/lm/grpc`, `llm.proto`'s `Run`); the wire protocol is unchanged, only the host-side call shape is. Registered future swaps (not yet implemented): **docker-exec** (attach to an already-running container's process via `docker exec -it`, for the container-isolation runtime) and **host-pty** (a bare local pty-spawned process, for a non-plugin engine). | `internal/vpio` (`Launcher`/`Session`/`ProcessSpec`/`ExitStatus`); go-plugin impl `internal/vpio/goplugin`; consumers `internal/cli/run.go`, `internal/cli/init.go` |
| **runner** | Everything after the wire: receives transmitted config/content, **materializes it locally** (the delivery seam), and drives the engine. Neutral about mechanism — it may spawn a process or call an API. | `internal/shared/agent` (`LaunchBackend`) + the per-engine backends |
| **engine** | What the runner drives to produce agent behavior — an agentic CLI product (claude-code, codex, gemini-cli) **or** a direct-API integration. Coined: unclaimed at this layer (elsewhere "engine" means an inference server). Continuity with the existing `agent_engine` key. | claude / codex / kiro / antigravity backends |
| **provider** / **model** | Standard sub-terms *beneath* an engine, for the model/API layer: `provider` = the vendor (Anthropic/OpenAI), `model` = the specific LLM. Industry-standard pair (Vercel AI SDK, opencode, Goose, Cline, LiteLLM, OpenRouter) — do not coin here. | (config for API-backed engines) |
| **loadout** | The full set of **surfaces** the control-plane assembles and the runner injects for a session — the composed delivery payload transmitted over the wire. | context assembly + `internal/lm/backends` (`AssembleManagedConfig`) |
| **surface** | One managed deliverable within a loadout — WHAT is delivered (the **context**, MCP, hooks, commands, skills and settings deliverables; `SurfaceKind` enumerates those the delivery chain dispatches on). Contrast **channel**, which is *how* the engine reaches it. | `ManagedConfig` fields + framed context + `.mcp.json` / `.claude/*` |
| **channel** | One way a composed **presentation** reaches the **engine**: the resolved path, argv, the environment, container mounts. Orthogonal to **surface** — a surface is *what* is delivered, a channel is *how the engine learns of or reaches it*, and one surface travels several channels at once (bytes at a path, the path named on argv, a home named in the environment). Each channel has its own **advice** interface and its own **terminal**. A channel is REQUIRED exactly when the composition declared a variant contributing to it; nothing states requiredness separately. | `internal/shared/agent/present` (`Presentation`'s fields) |
| **presentation** | The composed result for ONE surface: where its bytes are on the host, where the **engine** sees them, and everything the engine is told in order to find them. Built by composing **advice**, never selected from a set. | `present.Presentation` |
| **advice** | One link that contributes to or transforms a **channel**. An advice implements the interface of each channel it touches, and the implementing type IS the declaration — there is no variant→role table to drift, so `Build` discovers what a composition provides by type-asserting over the declared advice. An advice TRANSFORMS WHAT IT RECEIVES rather than recomputing from the original inputs; one that recomputes is not a link but a second root, and the advice before it may as well not have run. | `internal/shared/agent/present` (the `With*` chain) |
| **terminal** | What closes one **channel** by EFFECTING it for a given execution target. A channel has terminal ENTRIES rather than one terminal, because landing a channel is target-specific: writing the environment for a host process is a different act from writing it into a container, though both settle the same env channel. The entry matching the run's target is the one that executes. An entry is an EFFECTOR, never a transformer: by the time it runs the value is already correct, because the **advice** that chose the target computed it. That division keeps entries trivial and interchangeable, so a new target needs a new entry but no new remapping logic. A terminal also REFUSES, naming the channel nothing presented: the backstop for the gap between what a composition declared and what a bespoke advice actually did, not the primary detector, which is the type system. | `internal/shared/agent/present` (`Build`) |
| **command** | A **user-invoked** slash-command template (`/name`): the engine substitutes a prompt. Every engine has this under its own name (claude `.claude/commands/`, codex `$CODEX_HOME/prompts/`, opencode `.opencode/command/`, kiro `/name`). ctxloom's `command` item-kind and CLI group. | `ctxloom command`; `agent.CommandExport`; bundle `commands:` |
| **skill** | A **model-invoked** Agent Skill package: a directory containing `SKILL.md` (YAML frontmatter `name`+`description`, instructions body) plus optional bundled `scripts/`/assets, loaded by the engine via progressive disclosure when the description matches the task at hand — never typed by the user. Distinct item-kind from **command**; the two collide in the ecosystem word "skill" only for kiro (§3.3 of the skill/command split plan), reconciled by materializing both into `.kiro/skills/` under two separate manifests. | `ctxloom skill`; `bundles.BundleSkill`/`SkillPackage`; bundle `skills:`; `agent.SkillExport` |
| **context** | The model-facing instructions **surface** (the sysprompt / `CLAUDE.md` text). **Narrow** — one surface, never the umbrella (that's the loadout). Matches industry "context" = what's in the model's context window. | assembled context; framed sysprompt; `CLAUDE.md` |
| **agent** | A **ctxloom actor**: a profile-in-action — the primary you launch *and* each delegated worker (coordinator, finder, programmer, reviewer). What `run --agent` selects and what delegation spawns. **Reserved** — bare "agent" always means this. | the `subagent→agent` rename; `run --agent` |
| **engine agent** | The engine's *own* internal subagent (claude `--agent`, "agent family", the ACP `agent` field). Always qualified; never bare "agent." | claude `--agent`, ACP descriptor `agent` |
| **session** | A launched ctxloom run (harp-named). Hosts the primary agent and its delegated agents. | `~/.ctxloom/sessions/<harp>`; harp IDs |
| **profile** | An agent's *definition* (config). `agent` = profile-in-action. | `internal/config` profiles |
| **runtime coordinator** | The **process/library**: durable CQRS stores (run registry, role mailboxes, interaction journal), credential minting/verification, the agentcoord gRPC server (RunnerChannel/RunChannel), spawn-queue scheduling, and runner-loss synthesis. Hosted by every session-owning process (`ctxloom run`, `ctxloom acp`, the `ctxloom mcp serve` fallback). Never an LLM. | `internal/agentcoord/coord` |
| **coordinating agent** | The **LLM role**: an agent (usually the session's primary) that *uses* the coordination tools — spawning children (`agent_run`), routing their mail (`agent_send`/`agent_recv`), reading the roster, filing reports. Judgment lives here; process facts live in the runtime coordinator. | the parent session's model; the coordinator-ensemble profiles |

> Status: the `codex` and `kiro` engines above are implemented and hermetically tested; live operation is untested (no codex/kiro account on any dev host).

## Naming decisions (why these words)

- **"agent" is reserved for the ctxloom actor.** It was the most user-facing sense
  and matches the `subagent→agent` rename. Every other "agent" meaning gets a
  distinct name so bare "agent" is never ambiguous.
- **"engine" is a deliberate coinage.** There is *no* established, collision-free
  noun for "the CLI-product-or-direct-API backend a tool drives." The category
  words ("coding agent", "CLI agent", Zed/ACP "external agent") all collide with
  "agent." "engine" is unclaimed at this layer, so we use it.
- **"virtualized-process-io" names the role, not the transport.** go-plugin,
  docker-exec, and host-pty are three different ways to get bytes in and out
  of something that *behaves like* an interactive process (a pty-driven
  stdio/resize/signal/exit contract) even when — as with go-plugin today —
  there is no literal local process to attach to (it's a bidi RPC stream to
  an already-running subprocess). "vpio" names that shared behavioral
  contract so the transport can change without the frontend caring.
- **"provider" + "model" comply with industry** for the model/API sub-layer only.
  We do *not* stretch "provider" to mean the engine — established "provider" means
  the vendor (Anthropic), not the product (claude-code), and would imply API-first.
- **"context" is one surface, not the umbrella.** "context" was overloaded (the
  whole payload vs. the model-facing text). We reserve it for the model-facing
  instructions surface (industry usage), name each deliverable a **surface**, and
  call the composed set a **loadout**. So: surfaces compose into a loadout; context
  is the context surface.
- **ACP impedance:** ACP (a dependency) calls the driven backend **"agent"**
  (Zed: "external agent"). That is our **engine**, not our agent. We map ACP's
  "agent" → our "engine" at the boundary and never adopt ACP's noun internally.
- **"coordinator" is split, never bare.** The peer-model work made one word
  carry two natures: the **runtime coordinator** is deterministic
  infrastructure (journals, credentials, gRPC channels, lifecycle synthesis —
  it must never be confused with a model making judgment calls), while the
  **coordinating agent** is the LLM role driving delegation through the
  coordination tools. Self-narration by an agent is never load-bearing:
  process facts come from the runtime coordinator (runner channels,
  synthesized terminal records), judgment from the coordinating agent.
  Qualify every use; bare "coordinator" is ambiguous and reserved for
  headings where the qualifier is established.

## Implied code renames (consequences; schedule separately, not blockers)

- package `internal/shared/agent` → `runner` (biggest bare-"agent" offender).
- `agent_engine` config key → `engine`.
- ACP/descriptor `agent` field → `engine_agent`.
- audit `ctxloom agents` / `acp agents` — name each by whether it lists
  *ctxloom agents* or *engine agents*.
