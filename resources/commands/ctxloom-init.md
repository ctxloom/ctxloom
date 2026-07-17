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
- `ctxloom agent list` — existing agents (engine↔profile bindings)
- `ctxloom profile list` — existing local profiles
- `ctxloom llm list` — configured engines/models (the candidate `--engine`
  values for anything you bind later)
- `ctxloom container check` — whether containerized agents are viable here
- `ctxloom manage status` — hooks/MCP/statusline wiring per backend, plus
  which first-party companions (taskloom, ltk) are on PATH

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

I'll help you discover and set up context profiles, fragments, and commands
for your development workflow — and then bind the agents that will run them
(phase 5).

**Surface (read this first):**
- The configured remotes have already been cloned locally during init. Read
  the `ctxloom://remotes` MCP resource to see them.
- Use the **search_library** MCP tool to find matching bundles/profiles
  across ALL remotes. It reads the local clones (no network).
  - Search by tag: `tag:golang`, `tag:react`, `tag:docker`
  - Search by text: `security`, `testing`, `ci-cd`
  - Optionally pass item_type ("bundle" or "profile") to narrow.
  - Each result carries a `pull_ref` (e.g. `ctxloom-default/go-developer`) —
    that is the remote ref you reference from a local profile.
- `search_content` is for content ALREADY installed in this project; it does
  NOT reach remotes. Use search_library for discovery.

**Present your findings:**
1. What project type/stack you detected (phase 1 already scanned for this —
   reuse it, don't rescan)
2. Matching content (grouped by remote):
   - **Profiles**: Development workflow configurations
   - **Bundles**: Collections of fragments (context) and commands (reusable
     commands)
3. Ask the user which items to reference

**Reference selected items** by authoring a local profile, then pull. You
don't "install" remote content — you create a local profile that references
it:
- Inherit a remote **profile**: `ctxloom profile create <name> --parent
  <pull_ref>` (e.g. `ctxloom profile create go-dev --parent
  ctxloom-default/go-developer`)
- Reference a remote **bundle** (or one fragment): `ctxloom profile create
  <name> -b <pull_ref>` (optionally `#fragments/<frag>`)
- Then run `ctxloom remote pull` so every bundle/profile a profile references
  is fetched into the cache and the lockfile is updated.
- To pin a content version, append `@<git-tag-or-sha>` to the ref (e.g.
  `ctxloom-default/go-developer@v1.2.0`). Unpinned refs track the remote's
  default branch.

**Defaults:** the default context is the **default agent**'s composed
profiles. Bind a profile into an agent and point `default_agent` at it — e.g.
`ctxloom agent set dev --profiles <name>` then `ctxloom agent default dev` —
so a bare `ctxloom run` loads it automatically. An agent's profiles are a
list; each may be a local name or a remote ref. Confirm the final default
agent with the user.

**Review pending content:** anything pulled from a remote the user hasn't
seen before is held for review, not silently active. Run `ctxloom review`
(or `ctxloom review --list` to just see what's pending) and walk the user
through accepting or rejecting each item — this is the same review surface
you'd point them at any other time, not a separate setup-only step, so it's
fine to point it out again on a later reconfigure too.

**Then continue straight into agent setup (phase 5)** — profiles are only
half the picture: agents are named engine↔profile bindings that ctxloom
orchestrates (`ctxloom run --agent`, `ctxloom map`/`weave`), and the stack you
just detected plus the profiles you just created are their inputs. Don't end
the conversation between the two — keep it one continuous interview.

## Phase 5 — Agents

You are helping the user set up **ctxloom agents** — named, LOCAL bindings of
an **engine** (an LLM backend/model) to one or more **profiles** (composed
context), optionally with a **runtime** (host | container): where that
agent's engine process executes. Agents are what ctxloom orchestrates:
`ctxloom run --agent <name>` drives one interactively, and `ctxloom
map`/`ctxloom weave` fan a task across several in parallel. (Workspace
isolation — a git worktree per session — is NOT an agent property: it is
chosen per invocation with `--workspace` on run/map/weave, or the project
`workspace:` default.)

The **primary shape** is a small containerized ensemble: a **coordinator**
the user drives, plus **developer** and **finder** members it fans work to —
each member running in its own container. Lead with that shape, but the
palette is open and engine choice is the **user's** — you facilitate; you do
not impose a fixed recipe.

Agents are **local-only**: they live solely in this project's `.ctxloom`
(the `agents:` config key), are never shipped in bundles or remotes, and the
engine assignment is a cost/environment decision the user owns.

### Do this in three steps: SCAN → DISCUSS → SET

#### 1. SCAN what is actually available (don't guess; enumerate at runtime)

You already scanned engines/agents/containers in phase 1 and profiles in
phase 4 — reuse what you learned, don't re-scan the repo. Enumerate only what
is live config:

- **Engines** the user can assign: `ctxloom llm list` (the configured LLM
  labels/backends; the default is marked). These are the candidate
  `--engine` values.
- **Existing agents:** `ctxloom agent list` (don't duplicate; offer to
  refine what's there).
- **Container capability:** `ctxloom container check` — read its guidance.
  If it reports that containerized agents would degrade here (no runtime, or
  a dev container talking to the host's daemon), bind agents with `--runtime
  host` instead and tell the user what would enable containers (e.g. the dev
  container docker-in-docker feature).
- **Bundle tooling** (only if containers are viable): `ctxloom tooling` —
  trusted bundles may declare tools the agent image needs. Follow its
  instructions: show the user the proposed Containerfile diff and apply only
  what they explicitly approve, then `ctxloom container build`.

#### 2. DISCUSS — decide WITH the user which agents they want

**Start with the standard trio** — the ensemble the container-first workflow
expects. Propose all three, bound to the profiles from phase 4, and agree on
an engine for each:

- **coordinator** — the session the user actually drives; it plans,
  delegates, and fans `ctxloom map`/`ctxloom weave` across the other
  members. Bind the **most powerful** engine. Compose the local default
  coding profile. Launched with `ctxloom run --agent coordinator`.
- **developer** — the heavy implementer the coordinator delegates changes
  to. Bind a **powerful** engine and `--runtime container` so its engine
  runs contained. Compose the coding profile + the escalation-discipline
  fragment: significant changes (restructuring, interface/contract/API
  changes) get **escalated up to the coordinator** rather than decided
  autonomously. ctxloom-default ships that rule as
  `agent-roles#fragments/developer-escalation` — include it (or author an
  equivalent if unavailable).
- **finder** (locator/researcher) — cheap, parallel lookups that *find a
  thing by name and report findings straight back* (terse, no elaboration).
  Bind a **cheap** engine. The find-and-report-directly behavior is a
  profile fragment: ctxloom-default ships it as
  `agent-roles#fragments/finder` — include it (or author an equivalent
  short fragment if unavailable).

**Then offer the open palette** for anything else the user wants:

- **Code-review agents** — BUILD the review ensemble this project needs NOW
  and PERSIST it locally, so the `weave-review` command can fan it later.
  For each relevant lens × each language present in the repo, compose a
  tight, single-lens LOCAL review profile from the code-review FRAGMENT
  bundles — `code-review-base#fragments/conduct` +
  `code-review-base#fragments/thorough` +
  `code-review-<lens>#fragments/general` +
  `code-review-<lens>#fragments/<language>` (the `thorough` fragment makes
  the reviewer read every file rather than trust summaries/memories/names)
  (e.g. `ctxloom profile create cr-correctness-go -b <conduct-ref> -b
  <general-ref> -b <golang-ref>`). Prefer many small single-lens members
  (correctness / security / reliability / performance / testing / idioms /
  structural / …) over one giant reviewer. Bind an engine per lens (often a
  **cheaper** one — breadth over depth per lens); a bare profile name also
  works as a default-engine member. Also compose `cr-synthesis` as the
  reduce step (bind a high-power engine). These persist in the project's
  `.ctxloom`; `weave-review` then just fans them.
- **…and more** — docs writer, migration runner, test author, spec
  extractor, … Build each the same way: tight scope, a composed profile, an
  appropriate engine.

**Steer HARD toward tightly-scoped, single-responsibility members.** Many
small focused agents (one lens × language, one finder job) keep map/weave
fan-out cheap, parallel, and accurate. Actively discourage broad catch-all
agents — if the user proposes one big "do-everything" member, suggest
splitting it.

Match engines to roles as guidance, but it is the user's decision:
- cheap engine → finders/researchers and breadth review lenses,
- powerful engine → coordinator and developers/implementers,
- per-lens engine → code review (often cheaper than the developer).

#### 3. SET — write the agreed bindings

For each agent the user confirmed, write it to the local config:

```
ctxloom agent set <name> --engine <engine> --profiles <p1,p2,...> [--runtime host|container]
```

For example, the standard trio:

```
ctxloom agent set coordinator --engine <powerful> --profiles default
ctxloom agent set developer --engine <powerful> --profiles default,<developer-escalation profile> --runtime container
ctxloom agent set finder --engine <cheap> --profiles <finder profile>
```

- `--engine` is one of the labels from `ctxloom llm list` (omit it to use the
  project default / the profiles' own llm).
- `--profiles` is a comma-separated list of profile names/refs from the scan;
  they compose into one assembled context for that agent.
- `--runtime` sets where the agent's engine executes (omit to inherit the
  project default). `container` needs a reachable container runtime; if one
  isn't available here, leave the agent on the host and tell the user why.
- Workspace isolation is NOT set on agents: when the user fans parallel
  members that edit files, tell them to pass `--workspace worktree` to
  `ctxloom map`/`weave` (or set the project `workspace:` default) so each
  member session gets its own worktree.
- Re-running `set` with the same name updates the binding; `ctxloom agent
  remove <name>` deletes one.

Use names the user understands (e.g. a role name or language+lens). After
writing, run `ctxloom agent list` to confirm, and tell the user how to use
them: `ctxloom run --agent coordinator` to drive the coordinator, `ctxloom
map`/`ctxloom weave` to fan members (the `weave-review` command fans the
review members for the language(s) present).

If the user wants to stop, that's fine — they can run `/ctxloom-init` again
any time. Don't write any agent the user didn't agree to.

## Phase 6 — Close

Run `ctxloom setup verify` if it exists in this build and show the user its
report. (If the command isn't available yet, say so plainly and instead walk
through a manual checklist: `ctxloom manage status`, `ctxloom agent list`,
`ctxloom profile list`, `ctxloom llm list`, `ctxloom container check`, and the
client entry from phase 2 re-read.)

Then tell the user, plainly:
- **Exit this session.** This vendor CLI/TUI session was the bootstrap
  door — from now on, connect to ctxloom through the client you configured
  in phase 2.
- **`/ctxloom-init` reconfigures any time**, from any ordinary working
  session — nothing here was a one-shot wizard; come back whenever something
  needs to change (a new client, a new profile, a new agent).
