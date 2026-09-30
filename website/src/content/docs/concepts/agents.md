---
title: "Agents & Isolation"
---

Your `developer` profile is right for a quick question on your laptop and wrong for a long unattended run, the kind whose file writes you'd rather confine to the project instead of letting a stray `rm -rf` loose on your home directory. Editing the profile itself every time you want a different engine or runtime defeats the point of having a reusable profile at all.

An **agent** solves this by separating *what context* an AI receives (the profile's job) from *which engine runs it* and *where it executes*. Define `dev` once as `claude-code` on `runtime: container-rootless` with the `developer` profile, and `ctxloom run --agent dev` gets you that combination without touching the profile itself.

## What an agent is

```yaml
# .ctxloom/config.yaml
agents:
  finder:
    llm: claude-fast
    profiles: [finder]
  dev:
    llm: claude-code
    profiles: [default, go-developer]
    runtime: container-rootless
    permissions:
      mode: acceptEdits
      deny: ["Bash(git push *)"]
```

Agents are also the unit ctxloom's coordinator/child [delegation](/concepts/agent-delegation/)
resolves privileges from: a spawned child's MCP servers and permission mode come from its own
agent definition, never a coordinator's or a sibling's.

An agent names:

- **`llm`** — the LLM config label or engine to run. It overrides the constituent profiles' own `llm:`; omit it to use the project default.
- **`profiles`** — one or more profiles that compose into a single assembled context.
- **`runtime`** (optional) — where the engine process executes: `host`, `container-rootless`, or `container-rootful` (the two container values name WHO OWNS the container runtime daemon and are not interchangeable — a rootful daemon maps the engine's writes to a different uid than a rootless one). Omit to inherit the project's `runtime:` default.
- **`permissions`** (optional) — the agent's permission block: the engine-neutral fields, and one block per engine, keyed by the engine's name, holding that engine's own keys. Every field is optional:
  - `approver` — who answers a request the posture leaves open: `human` (the default), `none` (nobody: it is denied) or `reviewer` (the engine's own classifier, where the engine has one).
  - `approval_timeout` — how long a request waits before it is denied (`20m`; default 15m, at most 60m).
  - `sandbox` — what the engine's own commands may touch: `read-only`, `workspace-write` (the working tree only) or `full`. Undeclared, the engine's default applies; a value the engine cannot enforce where the agent runs is refused.
  - `network` — whether sandboxed commands may reach the network.
  - `<engine>:` — that engine's own keys, which it validates at load. For claude-code: `mode` (`default`, `acceptEdits`, `plan`, `bypass`), `after_plan` (the mode an approved plan continues at), and `allow`/`deny`/`ask` rules in claude's syntax (`Tool` or `Tool(content)`, e.g. `Bash(npm test)`, `mcp__server__tool`).

  ```yaml
  agents:
    worker:
      llm: claude-code
      permissions:
        approver: none
        sandbox: workspace-write
        claude-code:
          mode: acceptEdits
          deny: ["Bash(rm *)"]
  ```

  A binding's engine is only known when it resolves, so a binding may carry blocks for several engines; one that carries blocks but none for the engine it resolves to is refused (under `--degraded`, it runs at that engine's most restrictive posture). Each key the agent leaves empty inherits the engine label's, then — for the neutral fields — this project directory's [`permissions:` default](/guides/configuration/#permissions), then the engine's default. `run --permissions` overrides the mode for one session. A headless run (a one-shot or a delegated child) has nobody at the engine to answer a prompt, so the engine denies whatever its posture and rules leave open, and a delegated child's parent hears that turn as *blocked*.
- **`may_delegate`** (optional) — the agents this one may launch with `agent_run`. Unset or empty permits any; a list permits exactly those, and `agent_run` refuses any other, naming the ones allowed. A name that is no agent is refused at load. A child's permissions always come from its own binding: `agent_run` cannot set them.

Whether an agent gets the coordinator-only MCP tools (the ones that spawn, observe, control or stop other children, such as `agent_run`, `roster` and `agent_stop`) is **not** an agent-binding setting — it follows from where the agent sits in the delegation tree, not from anything you write on the binding. See the `delegation.depth` project setting on the [Configuration](/reference/config/) page: the session owner is depth 0 and always gets the tools; its subagents are depth 1 and, at the default cap, do not. A leaf still reports to its parent via `agent_send`/`agent_recv`/`agent_report`.

Agents live solely in your `.ctxloom`, under the `agents:` key of `config.yaml`. They are **never shipped in bundles or remotes**: bundles distribute portable context, but the engine choice (which costs money and holds your credentials) always stays yours.

## Managing agents

```bash
ctxloom agent create finder --llm claude-fast --profiles finder
ctxloom agent create dev --llm claude-code --profiles default,go-developer --runtime container-rootless --permissions acceptEdits
ctxloom agent create reviewer --profiles cr-correctness-go --permissions plan   # default engine
ctxloom agent list
ctxloom agent show dev
ctxloom agent remove reviewer --yes
```

`ctxloom agent edit <name>` changes an existing binding with the same flags.

`ctxloom init prompt` prints an interview prompt for your AI: it scans the available engines (`ctxloom llm list`) and profiles, discusses which roles you want (a coordinator, a containerized developer, a cheap finder, review lenses), and writes the bindings with `ctxloom agent create`. `ctxloom init` runs this as part of its setup interview; `init prompt` re-enters it any time.

## Using agents

```bash
ctxloom run --agent dev "implement the feature"          # one agent, interactive
```

A running coordinator fans work across several agents in parallel by spawning each as a child via the `agent_run` MCP tool (see [Agent Delegation](/concepts/agent-delegation/)) — each child runs on its own configured engine binding.

## The two isolation axes

Isolation is split into two independent axes, chosen at different times:

| Axis | Values | Set where | Governs |
|------|--------|-----------|---------|
| **Agent runtime** | `host` \| `container-rootless` \| `container-rootful` | On the agent (`agent create`/`agent edit --runtime`) or the project `runtime:` default | *Where the engine process executes* |
| **Session workspace** | `none` \| `worktree` | At invocation (`run --workspace`, or an `agent_run` spawn's `workspace` field) or the project `workspace:` default | *Which copy of the repo the session mutates* |

A binding also declares which **engine home** its engine runs against (the
directory holding the engine's login, memory, plugins and personal MCP
registrations): `engine_home: session`, a per-session home ctxloom controls, or
`engine_home: host`, the engine's real home. Leaving it unset means `session`.
A session home holds no credential. How the engine authenticates is the
agent's `auth:` setting:

- `login` shares your own login and its refresh: in place on the host, and in
  a container by mounting the directory that holds it (not on macOS, where it
  lives in the Keychain).
  `ctxloom init` gives the default agent this one.
- `token` (the default) uses a long-lived token you mint yourself with the
  engine's own flow and export. For claude: run `claude setup-token`, then
  export the token it prints as `CLAUDE_CODE_OAUTH_TOKEN` (or keep it in your
  secret manager and export it from there). A run with none exported is
  refused and tells you so.
- `api-key` uses a key you export (for claude, `ANTHROPIC_API_KEY`).
- `cloud` uses a cloud provider or gateway you have set up in your own shell
  (for claude: Amazon Bedrock, Google Vertex, Microsoft Foundry, Claude
  Platform on AWS, or a gateway's `ANTHROPIC_AUTH_TOKEN` and
  `ANTHROPIC_BASE_URL`).

ctxloom never collects, stores or mints a credential: it reads the declared
mode's credential from the environment it is launched in and hands it to the
agent. Anthropic does not allow a third party to "collect, store, or
intermediate Claude.ai credentials or session tokens"
([legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)).
`ctxloom auth status` shows whether each mode's credential is exported.

Only the declared mode's credential reaches the engine; the engine's other
credential variables are removed from the run's environment. An invalid choice (an
unknown mode, or one the agent's engine doesn't support) is refused when you
write it and when the agent launches, and the error lists the modes that
engine supports. `auth:` applies on `engine_home: host` too.

Agents on your subscription (`token` or `login`) draw from the same usage
limits as your own interactive use: Pro and Max limits are shared across
Claude and Claude Code
([Help Center](https://support.claude.com/en/articles/11145838-use-claude-code-with-your-pro-or-max-plan)),
and non-interactive `claude -p` runs, which is how ctxloom drives children,
draw from your subscription's limits
([Help Center](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan)).
A fan-out of agents spends them in parallel.

Selecting `engine_home: host` is the unsafe choice, because it hands the
engine your own login and registrations and lets it write them back, and the
launch says so. On the host it runs against your real `~/.claude` in place,
with claude's own lock, and copies nothing. In a container it means the
container's own fresh `$HOME`; your `~/.claude` is mounted into it only as an
`auth: login` agent's credential store.

The runtime axis is a property of the agent — a containerized developer stays containerized wherever it's used. The workspace axis is a property of the *session*: the same agent might work in the shared checkout for a quick question but in an isolated git worktree for a parallel fan-out where members would otherwise trample each other's edits.

```bash
ctxloom run --agent dev --workspace worktree "try the refactor"
```

A coordinator spawning several agents to work in parallel (e.g. `dev-a`, `dev-b`, each fixing its own module) sets `workspace: "worktree"` on each `agent_run` call so the children don't trample each other's edits.

## Containerized runtime

Agents with `runtime: container-rootless` or `runtime: container-rootful` run their engine inside a per-backend **agent image**. What that buys you is a **blast-radius boundary for filesystem writes**: the engine gets a fresh `$HOME`, so its global state stays out of yours, and the only part of your disk it can write is what ctxloom mounts — the project, the session's own transcript and artifact dirs, and the shared task log. A destructive command outside those paths hits the container's throwaway filesystem instead of your machine.

It is **not a security sandbox**, and you should not run untrusted content in it on that assumption. Specifically:

- **The network is not restricted.** ctxloom passes no network isolation flag; a containerized agent has the same egress your host does and can reach anything on it.
- **Your engine credential crosses the boundary.** The container gets the credential its agent's `auth:` mode resolves to, and for `login` and `cloud` the directories and files that hold it are mounted in: `login`'s read-write, so the agent can refresh or sign out your real login. The [environment reference](/reference/environment/#containerized-agents) lists what each mode forwards and mounts. The boundary does not stop the agent reading that credential or spending it.
- **Not every engine can run containerized.** An engine runs in a container only when it declares a container story of its own (how it installs into the agent image and what it needs there). For an engine that declares none, `ctxloom agent create`/`agent edit` **refuses** to write `runtime: container-rootless` or `runtime: container-rootful` for it and names the engines that do work, rather than accepting a binding whose every launch would then abort.
- **Some host state outside the project is mounted read-write.** The session's transcript store and persist dir under `~/.ctxloom/sessions/<harp>/`, and this project's task log `~/.ctxloom/tasks/<project-id>.jsonl` with its `.lock` sidecar — writable so in-container hooks, transcripts, and `taskloom` reach the one host store the session shares. The mount is those two **files**, not the `~/.ctxloom/tasks` directory: a run keyed to one project never sees another project's task log.

Use it to keep a long unattended run from wrecking your home directory. Do not use it as the thing standing between a prompt-injected agent and your API key or the internet.

```bash
ctxloom container check          # can containerized agents launch here?
ctxloom container build          # build/refresh the image for the default backend
ctxloom container scaffold       # materialize an editable base Containerfile
```

Images build in two stages: a shared **base** and a **composed agent stage** — one independently-cacheable install layer per engine (each via its own official installer), layered onto the base and content-keyed so identical (base, engine set) builds share one tag. ctxloom builds the image automatically when it's absent, whether launched via `run` or a delegated `agent_run` spawn.

You control the base, in this order (first one present wins):

1. `--base-image` overlays ctxloom onto an image that already ships the client CLI (skips the install entirely; single-engine).
2. `isolation_base_containerfile` / `--base-containerfile` builds the base from your own Containerfile.
3. **Your project's own `.devcontainer/devcontainer.json`** (or `.devcontainer.json`) is auto-detected as the base — "an isolated agent should run in the environment you develop in". Set `isolation_devcontainer_base: false` (or pass `--no-devcontainer-base` to `container build`) to opt out. `image:` and `build:` shapes are supported; `dockerComposeFile` needs `isolation_devcontainer_service` (or the devcontainer.json's own `service` key) to pick one service, since a multi-service compose project doesn't map to one agent container. Declared `features` are **not** honored (ctxloom does not depend on the devcontainer CLI) — a loud warning names what's skipped, and the build still proceeds from `image`/`build`.
4. The embedded default base (distro plus the coding-agent tool layer — git, ripgrep, curl, certs, jq).

An explicit base always beats auto-detection, and a devcontainer or user base that turns out unbuildable is a **fatal finding**, never a silent fallback to the default — the whole point is running in the environment you actually develop in, not a quietly different one.

`isolation_engines` selects which engine fragments compose into the image (default: every engine with a known official installer — "one instance can run any engine"); trim it to shrink the image. `ctxloom container scaffold` still writes an editable copy of the embedded default base and wires it into `isolation_base_containerfile` when you want to hand-edit the base itself.

`isolation_images` in config names fully user-provided images that run as-is and are never built. An override must honor the **identity contract**: it runs the ctxloom identity-remap entrypoint (base it on a ctxloom-built agent image, or install `ctxloom-entrypoint` as its `ENTRYPOINT`) and bakes no `USER` — otherwise the container would start with the image's own identity and root-own the files it writes into your mounted project. A violating image is a fatal startup finding; `--degraded` launches it anyway with the image's own identity.

`ctxloom container check` diagnoses the environment before you commit to containerized agents: whether this process is itself inside a container, which runtime (docker/podman) is reachable, whether the image exists, and whether the runtime's daemon shares your filesystem — the probe that catches docker-outside-of-docker setups where bind mounts silently resolve against the wrong filesystem. Run it inside a dev container to learn whether its agents can use the host's daemon through a mounted socket (docker-outside-of-docker: ctxloom joins its agents to its own container network, and every path they mount — the project, `~/.ctxloom` — must be on a bind mount or volume of the dev container), need docker-in-docker, or should stay on `runtime: host`.

### Tooling declarations

Companions (ltk, taskloom and the like) declare the tools their content needs inside the agent image, as a typed `tooling` entry in their loadout. `ctxloom container tooling` collects the declarations from **admitted** companions and emits them with instructions for your AI: propose the base-Containerfile additions as a diff, get your explicit approval per change, then rebuild. A rejected companion's declaration is withheld, and nothing is applied automatically on pull or sync.

## Agents vs profiles

| | Profile | Agent |
|---|---------|-------|
| Defines | Context (fragments, commands, MCP servers, variables) | Engine + profiles + runtime |
| Shipped in bundles | Yes (`<bundle>#profiles/<name>`) | Never — local only |
| Used by | `run -p`, agents | `run --agent`, `agent_run` |
| Engine choice | Optional `llm:` preference | Explicit `llm:` binding (overrides the profiles') |

A bare `-p` profile with `ctxloom run` is fine for a quick, unnamed context — reach for a named agent when you want a specific engine per role, a containerized runtime, a reusable role name, or the ability to spawn it as a delegated child (`agent_run` launches a *configured agent*, never a bare profile).
