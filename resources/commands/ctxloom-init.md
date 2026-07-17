---
description: Configure or reconfigure ctxloom for this project — ACP clients, companions, profiles, and agents
---

Welcome to ctxloom! You are running the ctxloom **setup interview** — the same
guidance whether this is a brand-new project (launched by `ctxloom init`
directly into your engine's own CLI/TUI) or a reconfigure mid-project
(invoked as `/ctxloom-init` in an ordinary working session). Six phases, in
order:

1. Orient + scan
2. ACP client(s) — **required outcome**
3. Companions (taskloom + ltk)
4. Profiles + content
5. Agents
6. Close

Work through them with the user conversationally. This is a **reconfigure**
tool, not a from-scratch wizard: phase 1 tells you what already exists, and
your standing rule for the rest of the interview is **reconfigure, don't
re-scaffold** — never re-propose, overwrite, or re-explain something already
in place unless the user actually wants to change it. If the user says
"skip", acknowledge and move on (or end here) — nothing below is mandatory
except what phase 2 calls out as required.

## Phase 1 — Orient + scan

**First, scan the current directory** for project indicators:
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
  profiles, hooks, trust — comes at Close, phase 6, once there's real
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
pulls phase 4 depends on), so a missing `git` is worth resolving before
anything else here.

Present a short summary: detected stack, what's already configured, what
looks missing. That summary is what makes the rest of this interview a
reconfigure instead of a re-scaffold — you now know what to leave alone.

## Phase 2 — ACP client(s) (REQUIRED — do this before anything else below)

This phase is not optional. **Setup is not done until at least one ACP
client is configured and working** — after this session ends (or after you
return control in a reconfigure), the client is the user's ONLY way to reach
ctxloom in steady state. Do this first, before companions/profiles/agents,
because everything after it is easier to demonstrate once a client is live.

### 2a. Detect what's installed

Do your own reconnaissance — this is agentic work, not a ctxloom command.
Check PATH for client binaries/CLIs and well-known config locations for the
clients on the roster below (and any the user names that aren't listed).
Report what you find installed vs. not, rather than assuming.

### 2b. Discuss

Ask the user which client(s) they want to use day to day. If their preferred
client isn't installed, offer to help install it: give the install command(s)
(brew/npm/download link/etc. per that client's own docs), but **the user runs
or approves the install themselves — ctxloom never installs a third-party
binary for you.**

Explain trade-offs from the roster below, not from assumption — and note
that the roster is a snapshot; if you can, verify current behavior rather
than trusting a stale line here.

**Client roster** (data, not code — this table is expected to grow over time
via ctxloom-default or a personal bundle, with no ctxloom release required;
for a client not listed here, read its own docs and apply the exact same
discipline below):

| Client | Detect | Docs | Notes (live findings) |
|---|---|---|---|
| Zed | `zed` on PATH; `~/.config/zed/settings.json` | zed.dev/docs | The ACP **reference client** — if a finding looks ambiguous, reproducing it against Zed tells you whether it's ctxloom's bug or the other client's. |
| Toad | `toad` on PATH (or its app config dir) | its own docs | Renders the agent's reasoning/"thinking" stream correctly. |
| Nori | `nori` on PATH; `~/.nori/cli/config.toml` | its own docs | As of the last live check, upstream Nori RECEIVES the thinking stream but renders a spinner instead of the text (a fix has been proposed upstream/as a fork) — don't promise thinking will be visible here without checking current behavior. |
| VSCode | the `formulahendry.acp-client` extension (or ctxloom's own, if installed) | the extension's marketplace page | Configure via VS Code's own settings UI/JSON for that extension. |

### 2c. Configure — the write discipline (read this before writing anything)

Two different configs are in play here, and they are never written the same
way:

- **ctxloom's own config** (anything under this project's `.ctxloom/`) —
  always via ctxloom's own CLI (`ctxloom agent set`, `ctxloom profile
  create`, …). Tested code, ctxloom's domain, never hand-edited.
- **The third-party client's config** (Zed's settings.json, Nori's
  config.toml, VSCode's extension settings, or any client not listed above) —
  configured by YOU (the agent), never by ctxloom, in this strict priority
  order:

  1. **PREFERRED: the client's OWN configuration CLI.** If the client ships
     a config command (`<client> config set …`, an `agents add` subcommand,
     whatever it calls it), USE IT. Discover it from that client's own
     `--help` or docs. The client's authors' own tested code owns its path,
     format, and merge behavior — that is strictly safer than you touching
     the file yourself, and it's the same "mimic the CLI's native surface"
     rule this project already follows for engines.
  2. **FALLBACK — only when the client ships no config CLI at all:** a
     direct file edit, but never freehand. Resolve the client's REAL
     per-OS config path yourself (from its docs — never trust an
     env-overridden `$HOME`, never guess), confirm the exact path and the
     exact change with the user, then delegate the actual filesystem
     mechanics to ctxloom's guarded primitive rather than writing the file
     yourself:

     ```
     echo '<json patch>' | ctxloom util config-write --file <resolved path> --filetype json
     ```

     (`--filetype toml` for a TOML target, e.g. Nori's config.toml.) This
     hidden command is TESTED CODE that does the dangerous part correctly
     every time: it backs the file up with a fresh timestamped copy before
     touching it, parses the existing file and deep-merges your patch
     (never truncates, never regenerates the file wholesale, every foreign
     key survives), refuses outright and leaves the original bytes
     untouched if the existing file fails to parse, and after writing
     re-reads the file and confirms your entry is actually there before
     reporting success. That is why the fallback is safe to use at all —
     the risky mechanics are code you call, not prose you followed by hand.
     Show the user the reported backup path.

  Never do both for the same fact, and never hand-roll the fallback
  yourself (no ad hoc file writes) — always go through `ctxloom util
  config-write` when there is no client config CLI.

- **What ctxloom gives you to write:** ctxloom emits only **frontend-neutral**
  connection info — it does not know or care what any client's format looks
  like. Get it from:

  ```
  ctxloom acp agents --format json
  ```

  Each entry has `name`, `command`, `args` (plus `agent`/`engine`/`profiles`
  when the entry is a named agent binding). You adapt that neutral fact into
  whatever shape the chosen client wants — a Zed `agent_servers` entry, a
  Nori `[agents.x]` TOML table, a VSCode extension setting, or the client
  registration format of an editor that doesn't exist yet. A permanent entry
  named after `ctxloom setup`/`/ctxloom-init` (this reconfigure door) is worth
  adding alongside whichever working agent(s) the user wants to reach day to
  day.

  The `command`/`args` pair the entry names doesn't have to be the local
  `ctxloom` binary. Two options, and it's the user's call which:
  - The local binary — what `--format json` reports — if `ctxloom` is on
    the user's PATH already.
  - **`npx -y ctxloom acp`** — zero-install: no local binary, nothing on
    PATH, and no version skew between what the client spawns and whatever
    actually shipped, once ctxloom is published to npm. Treat that "once
    published" as real — verify it's actually on npm before promising this
    works, the same live-check discipline as the client roster above; if
    it isn't yet, say so and fall back to the local-binary form.

- Standing rules regardless of path taken: never write anything the user
  hasn't agreed to; treat empty or failed output as a failure to surface
  (never claim a write happened without proof); on a re-run, merge with what
  you find rather than re-scaffolding.

### 2d. Verify — the exit criterion

Before moving on: at least one client entry must be **written, re-read back,
and confirmed well-formed** — and, if practical in this session, proven with
a live connect (have the user open that client and confirm a session starts).
Don't treat "I wrote something" as done; treat "I confirmed it's there and
it works" as done.

## Phase 3 — Companions: taskloom + ltk

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

## Phase 4 — Profiles + content

Find and reference the context this project needs — profiles, fragments,
commands — then move straight into phase 5 to bind the agents that use it;
don't break the conversation between the two, profiles are only half the
picture. Four beats, the same shape as phase 2:

### 4a. Discover

Remotes are already cloned locally — read `ctxloom://remotes`, then search
with the **search_library** MCP tool (by tag, e.g. `tag:golang`, or free
text) using the stack phase 1 already found; each result carries a
`pull_ref`. (`search_content` only reaches what's ALREADY installed — use
search_library for anything new.) Present a short, relevant list and ask
what to reference — a conversation, not a catalog to exhaustively enumerate.

### 4b. Reference

Author a local profile that points at what they picked — you don't
"install" remote content. `ctxloom profile create <name> --parent
<pull_ref>` inherits a remote profile; `-b <pull_ref>` pulls in a bundle (or
one fragment of it); a `@<tag-or-sha>` suffix on the ref pins a version. Then
`ctxloom remote pull` so the reference actually resolves and the lockfile
updates. `ctxloom profile create --help` has the exact flags.

### 4c. Default

Bind the chosen profile(s) into an agent and point `default_agent` there
(`ctxloom agent set dev --profiles <name>`, `ctxloom agent default dev`) so
a bare `ctxloom run` picks it up — confirm the choice with the user.

### 4d. Review

Anything pulled from a remote the user hasn't seen before is held, not
silently active. Run `ctxloom review` (`--list` for just the queue) and walk
them through accept/reject — a standing surface, not a one-time setup step,
so it's fine to point it out again on a later reconfigure too.

## Phase 5 — Agents

Bind **ctxloom agents** — named, LOCAL bindings of an **engine** (LLM
backend/model) to one or more **profiles**, optionally a **runtime**
(host | container) — right after phase 4, same conversation. `ctxloom run
--agent <name>` drives one; `ctxloom map`/`weave` fan a task across several.
Agents live only in this project's `.ctxloom` — never shipped in bundles or
remotes; engine choice is always the user's, you facilitate. (Workspace
isolation is a separate, per-invocation choice — `--workspace worktree` on
run/map/weave — not an agent property.) Work this the same shape as phase 2:
**SCAN → DISCUSS → SET**.

### 5a. Scan

Don't guess — you have this already from phase 1 (engines via `ctxloom llm
list`, existing agents via `ctxloom agent list`, container viability via
`ctxloom container check`) and phase 4 (profiles). If containers would
degrade here, plan on `--runtime host` and say why. If they're viable and a
bundle declares tooling needs, `ctxloom tooling` shows the proposed
Containerfile diff — apply only what's approved, then `ctxloom container
build`.

### 5b. Discuss

Lead with the standard trio, bound to the phase-4 profiles:
- **coordinator** — the session the user drives, delegating via
  `map`/`weave`. Most powerful engine.
- **developer** — the implementer the coordinator delegates to. Powerful
  engine, `--runtime container`, plus an escalation-discipline fragment
  (ctxloom-default ships `agent-roles#fragments/developer-escalation`, or
  author an equivalent) so structural/interface changes get escalated back
  up rather than decided alone.
- **finder** — cheap, parallel, terse: look something up and report
  straight back, no elaboration. Cheap engine, the
  `agent-roles#fragments/finder` fragment (or an equivalent).

Then the open palette. **Code-review agents** are the notable one: build the
ensemble THIS project needs now and persist it, so `weave-review` can fan it
later. The pattern is one tight, single-lens profile per lens × language
present — composed from the code-review base fragment plus that lens's and
that language's fragments (search_library surfaces the exact refs) — plus a
`cr-synthesis` reduce step on a high-power engine. Prefer many narrow lenses
over one broad reviewer. Beyond that: docs writer, migration runner, test
author, whatever the user wants — same pattern each time: tight scope, a
composed profile, a fitting engine.

Steer HARD toward tight, single-responsibility agents — many small focused
members keep map/weave fan-out cheap and accurate; push back if the user
proposes one big do-everything agent. As a rule of thumb: cheap engine →
finders and breadth review; powerful → coordinator/developers; per-lens →
code review (often cheaper than the developer).

### 5c. Set

Write what's agreed:

```
ctxloom agent set <name> --engine <engine> --profiles <p1,p2,...> --runtime host|container
```

(`ctxloom agent set --help` has the full flag list.) Worth knowing beyond
the syntax: `--profiles` composes several into one context; `--runtime
container` needs a reachable container runtime, else fall back to host and
say why; re-running `set` upserts, `agent remove <name>` deletes. Confirm
with `ctxloom agent list`, then tell the user how to use what they built
(`ctxloom run --agent coordinator`, `weave-review` for the review ensemble).

If they want to stop, that's fine — `/ctxloom-init` reconfigures any time.
Don't write anything the user didn't agree to.

## Phase 6 — Close

Run `ctxloom doctor` and show the user its report. This is the full
postcondition check — marker dir and config validity, the engine resolving
and authenticating, seeded deps locked and context assembling, hooks/MCP
registered, companions detected, and agents present with resolvable
profiles. Now that phases 1–5 have built real configured state, `doctor`
describes it end to end (unlike phase 1's scoped `--deps`, which only asked
"are the system tools present"). Walk the user through any `warn` lines and
help resolve them; then re-read the client entry from phase 2 to confirm it
landed.

Then tell the user, plainly:
- **Exit this session.** This vendor CLI/TUI session was the bootstrap
  door — from now on, connect to ctxloom through the client you configured
  in phase 2.
- **`/ctxloom-init` reconfigures any time**, from any ordinary working
  session — nothing here was a one-shot wizard; come back whenever something
  needs to change (a new client, a new profile, a new agent).
