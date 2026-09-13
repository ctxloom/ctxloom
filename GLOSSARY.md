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
| **engine** | What the runner drives to produce agent behavior — an agentic CLI product (claude-code, codex, gemini-cli) **or** a direct-API integration. Coined: unclaimed at this layer (elsewhere "engine" means an inference server). Continuity with the existing `agent_engine` key. | claude / codex / opencode backends |
| **provider** / **model** | Standard sub-terms *beneath* an engine, for the model/API layer: `provider` = the vendor (Anthropic/OpenAI), `model` = the specific LLM. Industry-standard pair (Vercel AI SDK, opencode, Goose, Cline, LiteLLM, OpenRouter) — do not coin here. | (config for API-backed engines) |
| **loadout** | The full set of **surfaces** the control-plane assembles and the runner injects for a session — the composed delivery payload transmitted over the wire. | context assembly + `internal/lm/backends` (`AssembleManagedConfig`) |
| **surface** | One managed deliverable within a loadout — WHAT is delivered (the **context**, MCP, hooks, commands, skills and settings deliverables; `SurfaceKind` enumerates those the delivery chain dispatches on). Contrast **channel**, which is *how* the engine reaches it. | `ManagedConfig` fields + framed context + `.mcp.json` / `.claude/*` |
| **channel** | One way a composed **presentation** reaches the **engine**: the resolved path, argv, the environment. Orthogonal to **surface** — a surface is *what* is delivered, a channel is *how the engine learns of or reaches it*, and one surface travels several channels at once (bytes at a path, the path named on argv, a home named in the environment). A channel is populated exactly when a presenter's composition names it (`AnnounceEnv`/`AnnounceFlag`); nothing states requiredness separately. | `internal/shared/agent/present` (`Presentation`'s fields) |
| **presentation** | The composed result for ONE surface: where its bytes are on the host, where the **engine** sees them, and everything the engine is told in order to find them. Built against an already-**advised** `Paths`, never selected from a set. | `present.Presentation` |
| **advice** | The rewrite of a `Paths` — every named **root**'s host-vs-engine sides and, for a container, the mounts that make the engine side true — applied ONCE, to the whole run, before any presenter composes anything. Two independent axes each rewrite one half of a root: a workspace advice rewrites Host (worktree materialization), a runtime advice rewrites Engine and records mounts (containerization); the identity host transport (`OnHost`) is the same `PathsAdvice` vocabulary as `Containerize`, not a branch a call site takes. Because advice finishes before a presenter runs, a presenter cannot be un-containerizable by the shape of what it announces — the old failure mode, where a channel needed its own advice interface and a flag-only presenter had no channel a container could discover a lever through. | `internal/shared/agent/present` (`PathsAdvice`, `Containerize`, `OnHost`) |
| **config modification record** | The sidecar file recording WHICH ENTRIES ctxloom currently has in a config file it SHARES with the user (`.ctxloom-managed`, beside the file it describes). It is what makes reconciling to a declared state possible in a file ctxloom does not own: the entries it names are removed and re-added each run, so a hand-authored entry in the same file survives untouched and one ctxloom no longer declares is withdrawn. CURRENT STATE, never a history — a surface ctxloom stops writing is cleared rather than grown, which is why "ledger" was the wrong noun for it. It exists as a SIDECAR because some engines forbid an in-file marker: Claude Code's settings schema is strict, so ctxloom cannot leave one in the file at all. | `internal/shared/ledger`; `.ctxloom-managed` |
| **command** | A **user-invoked** slash-command template (`/name`): the engine substitutes a prompt. Every engine has this under its own name (claude `.claude/commands/`, codex `$CODEX_HOME/prompts/`, opencode `.opencode/command/`). ctxloom's `command` item-kind and CLI group. | `ctxloom command`; `agent.CommandExport`; bundle `commands:` |
| **skill** | A **model-invoked** Agent Skill package: a directory containing `SKILL.md` (YAML frontmatter `name`+`description`, instructions body) plus optional bundled `scripts/`/assets, loaded by the engine via progressive disclosure when the description matches the task at hand — never typed by the user. Distinct item-kind from **command**. | `ctxloom skill`; `bundles.BundleSkill`/`SkillPackage`; bundle `skills:`; `agent.SkillExport` |
| **context** | The model-facing instructions **surface** (the sysprompt / `CLAUDE.md` text). **Narrow** — one surface, never the umbrella (that's the loadout). Matches industry "context" = what's in the model's context window. | assembled context; framed sysprompt; `CLAUDE.md` |
| **agent** | A **ctxloom actor**: a profile-in-action — the primary you launch *and* each delegated worker (orchestrator, finder, programmer, reviewer). What `run --agent` selects and what delegation spawns. **Reserved** — bare "agent" always means this. | the `subagent→agent` rename; `run --agent` |
| **engine agent** | The engine's *own* internal subagent (claude `--agent`, "agent family"). Always qualified; never bare "agent." | claude `--agent` |
| **session** | A launched ctxloom run (harp-named). Hosts the primary agent and its delegated agents. | `~/.ctxloom/sessions/<harp>`; harp IDs |
| **profile** | An agent's *definition* (config). `agent` = profile-in-action. | `internal/config` profiles |
| **runtime coordinator** | The **process/library**: durable CQRS stores (run registry, role mailboxes, interaction journal), credential minting/verification, the agentcoord gRPC server (RunnerChannel/RunChannel), spawn-queue scheduling, and runner-loss synthesis. Hosted by every session-owning process (`ctxloom run`, the `ctxloom mcp serve` fallback). Never an LLM. | `internal/agentcoord/coord` |
| **orchestrating agent** (*orchestrator*) | The **LLM role**: an agent (usually the session's primary) that *uses* the coordination tools — spawning children (`agent_run`), routing their mail (`agent_send`/`agent_recv`), reading the roster, filing reports. Judgment lives here; process facts live in the runtime coordinator. Formerly "coordinating agent" — renamed 2026-09-12, see the naming decision below. | the `orchestrator` agent binding; the agent-ensemble profiles |
| **originator** | The process a human launches (`ctxloom run`). It hosts the runtime coordinator, and it is the ONLY process that ever execs a container runtime — every spawn below it is performed here, on a requester's behalf. | `ctxloom run`; `internal/agentcoord/coord` |
| **executor** | An agent the orchestrator spawns to do work, including HEAVY work. Orchestrators dispatch the full suite, acceptance and mutation to executors and consume the verdict rather than running them — that is what keeps every heavy job countable against `delegation.concurrency`. | the `developer` binding; `delegation.concurrency` |
| **subagent** | A light agent (find/search) that serves an executor. A PEER of the executors, not below them — same delegation depth. The executor REQUESTS it; the ORCHESTRATOR spawns it. Never spawned by the executor itself — the tree stays flat. | `agent_run`; `Coordinator.AgentRun` |

> Status: the `codex` engine above is implemented and hermetically tested; live operation is untested (no codex account on any dev host).

## Naming decisions (why these words)

- **"agent" is reserved for the ctxloom actor.** It was the most user-facing sense
  and matches the `subagent→agent` rename. Every other "agent" meaning gets a
  distinct name so bare "agent" is never ambiguous.
- **"engine" is a deliberate coinage.** There is *no* established, collision-free
  noun for "the CLI-product-or-direct-API backend a tool drives." The category
  words ("coding agent", "CLI agent", Zed's "external agent") all collide with
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
- **The AGENT is the orchestrator; the COORDINATOR is the runtime.** One word
  carried two natures: the **runtime coordinator** is deterministic
  infrastructure (journals, credentials, gRPC channels, lifecycle synthesis —
  it must never be confused with a model making judgment calls), while the
  LLM role driving delegation is the **orchestrating agent**. Self-narration by
  an agent is never load-bearing: process facts come from the runtime
  coordinator (runner channels, synthesized terminal records), judgment from
  the orchestrating agent.

  WHY A DISTINCT WORD RATHER THAN A QUALIFIER (changed 2026-09-12). This entry
  previously ruled "qualify every use; bare coordinator is ambiguous". That
  discipline was tried and did NOT hold, and the evidence is in the tree: the
  agent binding and `default_agent` were both a bare `coordinator`, the role
  fragment was titled "Role: Coordinator", and a topology study written on
  2026-09-12 had to keep saying "where the RUNTIME coordinator sits" to stay
  unambiguous — its central open question was ambiguous as posed. A rule that
  needs constant discipline from every writer is not a rule; it is a hope.
  Two natures now have two words, so no qualifier is needed and none can be
  forgotten.

  Bare "coordinator" therefore means the RUNTIME component, and `coord.*`
  symbols keep their names deliberately — they are that component. An agent
  is never a coordinator.

- **The topology is FOUR LEVELS and does not recurse** (ruled 2026-09-12).
  originator → orchestrator → executors → subagents.

  THOSE FOUR NAMES ARE ROLES, NOT DEPTHS, and the distinction is load-bearing
  enough to state: originator and orchestrator describe WHERE A PROCESS RUNS
  (containment), while executor and subagent describe WHAT AN AGENT IS FOR
  (role and service). Read as a ladder they mislead, because a subagent does
  NOT sit below an executor — the executor only REQUESTS it and the orchestrator
  spawns it, so the two are PEERS.

  There are exactly TWO agent depths, and that is the fact the mechanism uses:
  the orchestrating agent is depth 0 — containerized or not, since a top-level
  container run is enqueued at the owner's depth — and executors and subagents
  are both depth 1, leaves under the default `delegation.depth`. Everything in
  the section below turns on there being two, so do not infer a third from the
  names. An orchestrator MAY itself
  run in a container ("level 2"), and that shape is **2a**: the orchestrating
  agent is an ordinary depth-1 child cell, while the runtime coordinator stays
  in the originator. Its container therefore needs no container runtime, no
  socket, and no nested daemon.

  **2b — a nested `ctxloom run` owning its own runtime coordinator — is OUT OF
  SCOPE, not merely deferred.** The two readings look identical when written as
  "a container for the orchestrator", so name the shape, never the phrase.

  WHY TRUE RECURSION IS UNWANTED (ruled 2026-09-12, after a feasibility study
  and an independent adversarial review of it). Not because it is hard. Because
  FLATNESS IS LOAD-BEARING: four properties this design relies on are bought by
  having exactly two agent depths — the orchestrator, and everything it spawns.

  1. POSITION IMPLIES PRIVILEGE. `runnerIsLeaf` decides delegation from depth
     against `delegation.depth`. That works only while there are two depths. Add
     recursion and there is an orchestrator at depth n and executors at n+1 for
     every n, so no global cap can express "orchestrators may delegate,
     executors may not" — which revives the per-binding flag
     `agents.RetiredCoordinatorKey` deliberately REMOVED in favour of position.
     That retirement is correct only because the tree is flat.

  2. ONE CAP SEES EVERYTHING. Heavy work is bounded because a single
     `Coordinator.slots` observes the whole tree — which is why an orchestrator
     dispatches gates to executors instead of running them. With a runtime
     coordinator per level, each caps its own children and nothing holds a
     global view.

  3. ONE SWEEPER RECLAIMS EVERYTHING. Orphan reclaim is keyed on
     `ctxloom.owner-pid` plus a liveness check, and PID LIVENESS DOES NOT CROSS
     A NAMESPACE BOUNDARY: a nested container's owner pid is a pid inside its
     parent, unevaluable from outside. Every level would need its own sweeper,
     and a level that dies takes its children's reaper with it. Unreclaimable by
     construction.

  4. IDENTITY PATH MAPPING STAYS CORRECT, and `renderRunSpec` has no privilege
     knob at all — no `--device`, no `--security-opt`, no `--privileged`. That
     absence is a PROPERTY, not a gap: no agent cell can be granted a nested
     runtime by configuration. Recursion means deliberately reopening it.

  The decisive argument is that properties 2 and 3 are exactly the two failures
  that have actually occurred here: an OOM took a whole run when five concurrent
  heavy jobs ran against a cap that could see three, and the same kill stranded
  three worktrees that only manual recovery found. Recursion makes both
  structurally worse, in exchange for a capability nothing has yet needed.

  THE TRAP TO WATCH FOR: someone reaching for recursion to solve a problem that
  a flat tree plus the per-session exchange directory already solves — an
  executor wanting work done without spending the orchestrator's context does
  NOT need to spawn anything itself. If recursion is ever revisited, price all
  four properties above; the full analysis is in the container-topology study.

  Note that delegation privilege is DERIVED, never declared: a per-binding
  `coordinator: true` flag existed and was deliberately REMOVED
  (`agents.RetiredCoordinatorKey`) in favour of position in the tree. Any
  proposal to re-add a per-agent "may delegate" field is reviving something
  this codebase retired on purpose — read that constant's doc before doing it.

## Implied code renames (consequences; schedule separately, not blockers)

- package `internal/shared/agent` → `runner` (biggest bare-"agent" offender).
- `agent_engine` config key → `engine`.
- audit `ctxloom agents` — name each by whether it lists
  *ctxloom agents* or *engine agents*.
