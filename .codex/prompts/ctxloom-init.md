---
description: Configure or reconfigure ctxloom for this project — companions, profiles, and agents
---


Welcome to ctxloom! You are running the ctxloom **setup interview** — the same
guidance whether this is a brand-new project (launched by `ctxloom init`
directly into your engine's own CLI/TUI) or a reconfigure mid-project
(invoked as `/ctxloom-init` in an ordinary working session). Five phases, in
order:

1. Orient + scan
2. Companions (taskloom + ltk)
3. Profiles + content
4. Agents
5. Close

Work through them with the user conversationally. This is a **reconfigure**
tool, not a from-scratch wizard: phase 1 tells you what already exists, and
your standing rule for the rest of the interview is **reconfigure, don't
re-scaffold** — never re-propose, overwrite, or re-explain something already
in place unless the user actually wants to change it. If the user says
"skip", acknowledge and move on — nothing below is mandatory.

## Phase 1 — Open, orient, scan

**Open the interview by orienting the user** — this is the first thing they
see, and they may never have used ctxloom. Lead with this (say it in your own
voice, but keep the substance and the specifics; deliver it briefly, don't
belabor it):

> I'll help you set ctxloom up for this project. ctxloom's primary interface
> is its own **CLI/TUI** — `ctxloom run` drives your coding engine directly,
> with **assembled context** as the difference from a bare engine: your
> standards, tools, and commands as versioned, shareable *profiles* and
> *bundles* composed into every session, not a hand-kept CLAUDE.md; **many
> engines behind one interface** — bind different agents to different
> engines and models and switch without relearning each vendor's CLI;
> **per-agent isolation** — agents run in containers or git worktrees, so a
> delegated agent can't reach your host or another agent's state;
> **cross-engine delegation** — a coordinator agent spawns and collects work
> from child agents, even ones on a different engine; and **signed,
> trust-verified context** — bundles pulled from shared remotes carry
> signatures from publishers you've chosen to trust. If you also want to
> reach ctxloom from an editor's AI panel (Zed, VS Code, and other ACP
> clients), that's available too, as an optional add-on we can set up later
> — it's not required to get a working setup today.
>
> Setup takes a few minutes: we'll wire up companions, choose your profiles
> and agents, and verify it all.

Then lead straight into the scan — don't recite the phase list mechanically.

**Scan the current directory** for project indicators:
- go.mod, Cargo.toml, package.json, pyproject.toml, requirements.txt
- Dockerfile, docker-compose.yml, Makefile, justfile
- .github/ and other CI/CD configs
- Framework-specific files (next.config.js, vite.config.ts, etc.)

**Then check what ctxloom already has configured** — don't guess, enumerate:
- `ctxloom doctor --deps` — confirm the system tools ctxloom depends on are
  present (`git`, `ssh-keygen` for signing, a container runtime). A fresh
  `ctxloom init` already checked this up front (init needs `git` to clone),
  so this is mainly for the reconfigure path (`/ctxloom-init` in an existing
  session, which skipped init) — worth re-confirming here, cheaply, before
  anything below depends on them. (The FULL `ctxloom doctor` report — agents,
  profiles, hooks, trust — comes at Close, phase 5, once there's real
  configured state for it to describe; running it now would just be a wall
  of "not set up yet" on a brand-new project.)
- `ctxloom agent list` — existing agents (engine↔profile bindings)
- `ctxloom profile list` — existing local profiles
- `ctxloom llm list` — configured engines/models (the candidate `--engine`
  values for anything you bind later)
- `ctxloom container check` — whether containerized agents are viable here
- `ctxloom manage status` — hooks/MCP/statusline wiring per backend, plus
  which first-party companions (taskloom, ltk) are on PATH

If the dep check reports something missing, surface it to the user and
guide them to install it themselves (their own package manager, or the
tool's docs) before continuing — later phases depend on these. `git` is the
notable one: it's close to a hard prerequisite (worktrees, and the remote
pulls phase 3 depends on), so a missing `git` is worth resolving before
anything else here.

Present a short summary: detected stack, what's already configured, what
looks missing. That summary is what makes the rest of this interview a
reconfigure instead of a re-scaffold — you now know what to leave alone.

## Phase 2 — Companions: taskloom + ltk

ctxloom ships alongside two first-party companion binaries that extend it
without any ctxloom code change:

- **taskloom** — per-project task tracking (the `task_list`/`task_add`/
  `task_set_status`/`task_edit` MCP tools, plus the `taskloom` CLI).
- **ltk** — a command-redirect pre-tool hook that intercepts risky shell
  invocations before they run.

**Detect:** check PATH yourself (`which taskloom`, `which ltk`) and/or run
`ctxloom manage status`, which reports each companion's presence and what it
enables (and what's disabled without it).

**Explain + guide install** for anything missing — give the install command
(e.g. `brew install ctxloom/tap/taskloom`, `brew install ctxloom/tap/ltk`),
but the user runs it themselves; ctxloom never installs a binary for you.

**Verify:** once a companion is on PATH, its loadout is picked up
automatically (no separate registration step) — `ctxloom manage status`
should now show it, and `ctxloom manage hooks status` should show its hooks
applied to the backends in use. ltk self-installs its own pre-tool hook the
first time it's invoked; taskloom's MCP tools ride the same `ctxloom manage
hooks install` pass that wires ctxloom's own MCP server.

## Phase 3 — Profiles + content

Find and reference the context this project needs — profiles, fragments,
commands — then move straight into phase 4 to bind the agents that use it;
don't break the conversation between the two, profiles are only half the
picture. Four beats, the same shape as phase 2:

### 3a. Discover

Remotes are already cloned locally — read `ctxloom://remotes`, then search
with the **search_library** MCP tool (by tag, e.g. `tag:golang`, or free
text) using the stack phase 1 already found; each result carries a
`pull_ref`. (`search_content` only reaches what's ALREADY installed — use
search_library for anything new.) Present a short, relevant list and ask
what to reference — a conversation, not a catalog to exhaustively enumerate.

### 3b. Reference

Author a local profile that points at what they picked — you don't
"install" remote content. `ctxloom profile create <name> --parent
<pull_ref>` inherits a remote profile; `-b <pull_ref>` pulls in a bundle (or
one fragment of it); a `@<tag-or-sha>` suffix on the ref pins a version. Then
`ctxloom remote pull` so the reference actually resolves and the lockfile
updates. `ctxloom profile create --help` has the exact flags.

### 3c. Default

Bind the chosen profile(s) into an agent and point `default_agent` there
(`ctxloom agent create dev --profiles <name>`, `ctxloom agent default dev`) so
a bare `ctxloom run` picks it up — confirm the choice with the user.

### 3d. Review

Anything pulled from a remote the user hasn't seen before is held, not
silently active. Run `ctxloom review` (`--list` for just the queue) and walk
them through accept/reject — a standing surface, not a one-time setup step,
so it's fine to point it out again on a later reconfigure too.

## Phase 4 — Agents

Bind **ctxloom agents** — named, LOCAL bindings of an **engine** (LLM
backend/model) to one or more **profiles**, optionally a **runtime**
(host | container) — right after phase 3, same conversation. `ctxloom run
--agent <name>` drives one directly; a running coordinator fans work across
several by spawning them as children with the `agent_run` MCP tool. Agents
live only in this project's `.ctxloom` — never shipped in bundles or
remotes; engine choice is always the user's, you facilitate. (Workspace
isolation is a separate, per-invocation choice — `--workspace worktree` on
`run`/`acp`, or the `workspace` field on an `agent_run` spawn — not an agent
property.) Work this the same shape as phase 3: **SCAN → DISCUSS → SET**.

### 4a. Scan

Don't guess — you have this already from phase 1 (engines via `ctxloom llm
list`, existing agents via `ctxloom agent list`, container viability via
`ctxloom container check`) and phase 3 (profiles). If containers would
degrade here, plan on `--runtime host` and say why. If they're viable and a
bundle declares tooling needs, `ctxloom container tooling` shows the proposed
Containerfile diff — apply only what's approved, then `ctxloom container
build`.

### 4b. Discuss

Lead with the standard trio, bound to the phase-3 profiles:
- **coordinator** — the session the user drives, delegating to children via
  `agent_run`. Most powerful engine.
- **developer** — the implementer the coordinator delegates to. Powerful
  engine, `--runtime container`, plus an escalation-discipline fragment
  (ctxloom-default ships `agent-roles#fragments/developer-escalation`, or
  author an equivalent) so structural/interface changes get escalated back
  up rather than decided alone.
- **finder** — cheap, parallel, terse: look something up and report
  straight back, no elaboration. Cheap engine, the
  `agent-roles#fragments/finder` fragment (or an equivalent).

Then the open palette. **Code-review agents** are the notable one: build the
ensemble THIS project needs now and persist it, so the coordinator can fan it
out later via `agent_run`. The pattern is one tight, single-lens profile per
lens × language present — composed from the code-review base fragment plus
that lens's and that language's fragments (search_library surfaces the exact
refs). Prefer many narrow lenses over one broad reviewer; the coordinator
reads each child's `agent_report` back and synthesizes the findings itself —
no separate reduce agent needed. Beyond that: docs writer, migration runner,
test author, whatever the user wants — same pattern each time: tight scope, a
composed profile, a fitting engine.

Steer HARD toward tight, single-responsibility agents — many small focused
children keep `agent_run` fan-out cheap and accurate; push back if the user
proposes one big do-everything agent. As a rule of thumb: cheap engine →
finders and breadth review; powerful → coordinator/developers; per-lens →
code review (often cheaper than the developer).

### 4c. Set

Write what's agreed:

```
ctxloom agent create <name> --engine <engine> --profiles <p1,p2,...> --runtime host|container
```

(`ctxloom agent create --help` has the full flag list.) Worth knowing beyond
the syntax: `--profiles` composes several into one context; `--runtime
container` needs a reachable container runtime, else fall back to host and
say why; change an existing one with `agent edit <name>`, drop one with
`agent delete <name>`. Confirm
with `ctxloom agent list`, then tell the user how to use what they built
(`ctxloom run --agent coordinator`, which fans the review ensemble out via
`agent_run` when asked).

If they want to stop, that's fine — `/ctxloom-init` reconfigures any time.
Don't write anything the user didn't agree to.

## Phase 5 — Close

Run `ctxloom doctor` and show the user its report. This is the full
postcondition check — marker dir and config validity, the engine resolving
and authenticating, seeded deps locked and context assembling, hooks/MCP
registered, companions detected, and agents present with resolvable
profiles. Now that phases 1–4 have built real configured state, `doctor`
describes it end to end (unlike phase 1's scoped `--deps`, which only asked
"are the system tools present"). Walk the user through any `warn` lines and
help resolve them.

Then tell the user, plainly:
- **You have a working setup.** `ctxloom run` (or `ctxloom run --agent
  <name>`) is the primary way to reach ctxloom from here — this vendor
  CLI/TUI session was just the bootstrap door.
- **Want to reach ctxloom from an editor's AI panel too?** That's optional
  and separate from this interview — invoke the **acp-setup** skill (or ask
  me to) any time to configure ctxloom as an ACP server for a client like
  Zed/VSCode, or as an ACP client to a different ACP-speaking agent.
- **`/ctxloom-init` reconfigures any time**, from any ordinary working
  session — nothing here was a one-shot wizard; come back whenever something
  needs to change (a new profile, a new agent, a new companion).
