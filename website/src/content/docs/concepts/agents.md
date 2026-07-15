---
title: "Agents & Isolation"
---

Your `developer` profile is right for a quick question on your laptop and wrong for a long unattended run, the kind whose file writes you'd rather confine to the project instead of letting a stray `rm -rf` loose on your home directory. Editing the profile itself every time you want a different engine or runtime defeats the point of having a reusable profile at all.

An **agent** solves this by separating *what context* an AI receives (the profile's job) from *which engine runs it* and *where it executes*. Define `dev` once as `claude-code` on `runtime: container` with the `developer` profile, and `ctxloom run --agent dev` gets you that combination without touching the profile itself. Agents are also the members that [map and weave](/concepts/weave/) fan work across.

## What an agent is

```yaml
# .ctxloom/config.yaml
agents:
  finder:
    engine: claude-fast
    profiles: [finder]
  dev:
    engine: claude-code
    profiles: [default, go-developer]
    runtime: container
    permissions: acceptEdits
    escalation:
      - kinds: [FILE_CHANGE]
        action: auto_accept
      - action: relay_to_role   # everything else goes up
        role: parent
        timeout: 5m
```

An agent names:

- **`engine`** — the LLM config label or backend to run. It overrides the constituent profiles' own `llm:`; omit it to use the project default.
- **`profiles`** — one or more profiles that compose into a single assembled context.
- **`runtime`** (optional) — where the engine process executes: `host` or `container`. Omit to inherit the project's `runtime:` default.
- **`permissions`** (optional) — the launch-time permission posture the engine starts in: `default`, `acceptEdits`, `plan`, or `bypass`. Omit to inherit the engine label's configured posture, then the built-in default. `run --permissions` overrides it for one session.
- **`escalation`** (optional, config-file only) — an ordered ladder of rungs deciding what happens when the agent raises an approval request. Each rung names the request `kinds` it matches (`COMMAND_EXECUTION`, `FILE_CHANGE`, `TOOL_USE`, `PERMISSION_ESCALATION`, `ARTIFACT_REVIEW`, `CUSTOM`; empty matches all), an `action` (`auto_accept`, `auto_decline`, `relay_to_role`, `surface_to_human`), a `role` for the relaying actions (only `parent` today), and a `timeout` after which a relayed request falls through to the next matching rung. The ladder bottoms out at *decline* when no rung resolves a request. Omit it and the ladder is derived from `permissions`.

Agents live solely in your `.ctxloom` — under the `agents:` key of `config.yaml` or as `.ctxloom/agents/<name>.yaml` files. They are **never shipped in bundles or remotes**: bundles distribute portable context, but the engine choice (which costs money and holds your credentials) always stays yours.

## Managing agents

```bash
ctxloom agent set finder --engine claude-fast --profiles finder
ctxloom agent set dev --engine claude-code --profiles default,go-developer --runtime container --permissions acceptEdits
ctxloom agent set reviewer --profiles cr-correctness-go --permissions plan   # default engine
ctxloom agent list
ctxloom agent show dev
ctxloom agent remove reviewer
```

Re-running `agent set` with the same name updates the binding. `agent set` covers every field except `escalation`, which has no flag — write the ladder into `config.yaml` or the agent's own `.ctxloom/agents/<name>.yaml`.

`ctxloom agent setup` prints an interview prompt for your AI: it scans the available engines (`ctxloom llm list`) and profiles, discusses which roles you want (a coordinator, a containerized developer, a cheap finder, review lenses), and writes the bindings with `ctxloom agent set`. `ctxloom init` runs this as part of its setup interview; `agent setup` re-enters it any time.

## Using agents

```bash
ctxloom run --agent dev "implement the feature"          # one agent, interactive
ctxloom map --agents finder,reviewer "assess the parser" # parallel members
ctxloom weave --agents cr-security,cr-perf -s synthesis "review this diff"
ctxloom acp --agent dev                                  # serve over ACP (editors)
```

In `map`/`weave`, `--agents` members each run on their own engine binding, while bare `-p` profiles are sugar for a default-engine agent. `ctxloom acp agents` prints one editor agent-server entry per binding so ACP clients (like Zed) can pick agents from the editor.

## The two isolation axes

Isolation is split into two independent axes, chosen at different times:

| Axis | Values | Set where | Governs |
|------|--------|-----------|---------|
| **Agent runtime** | `host` \| `container` | On the agent (`agent set --runtime`) or the project `runtime:` default | *Where the engine process executes* |
| **Session workspace** | `none` \| `worktree` | At invocation (`run`/`map`/`weave --workspace`) or the project `workspace:` default | *Which copy of the repo the session mutates* |

The runtime axis is a property of the agent — a containerized developer stays containerized wherever it's used. The workspace axis is a property of the *session*: the same agent might work in the shared checkout for a quick question but in an isolated git worktree for a parallel fan-out where members would otherwise trample each other's edits.

```bash
ctxloom run --agent dev --workspace worktree "try the refactor"
ctxloom map --agents dev-a,dev-b --workspace worktree "each fix one module"
```

## Containerized runtime

Agents with `runtime: container` run their engine inside a per-backend **agent image**. What that buys you is a **blast-radius boundary for filesystem writes**: the engine gets a fresh `$HOME`, so its global state stays out of yours, and the only part of your disk it can write is what ctxloom mounts — the project, the session's own transcript and artifact dirs, and the shared task log. A destructive command outside those paths hits the container's throwaway filesystem instead of your machine.

It is **not a security sandbox**, and you should not run untrusted content in it on that assumption. Specifically:

- **The network is not restricted.** ctxloom passes no network isolation flag; a containerized agent has the same egress your host does and can reach anything on it.
- **Your engine credentials cross the boundary.** The container gets either the engine's scoped env passthrough (`ANTHROPIC_*` for claude when `ANTHROPIC_API_KEY` is set, `KIRO_API_KEY` for kiro) or the engine's credential files bind-mounted **read-only** into the fresh `$HOME`. Read-only stops the agent rewriting them; it does not stop it reading them, and it does not stop it spending them.
- **Some host state outside the project is mounted read-write.** The session's transcript store and persist dir under `~/.ctxloom/sessions/<harp>/`, and the per-project task log under `~/.ctxloom/tasks/` — writable so in-container hooks, transcripts, and `taskloom` reach the one host store the session shares.

Use it to keep a long unattended run from wrecking your home directory. Do not use it as the thing standing between a prompt-injected agent and your API key or the internet.

```bash
ctxloom container check          # can containerized agents launch here?
ctxloom container build          # build/refresh the image for the default backend
ctxloom container scaffold       # materialize an editable base Containerfile
```

Images build in two stages: a shared **base** and a **composed agent stage** — one independently-cacheable install layer per engine (antigravity, claude-code, codex, kiro, opencode today, each via its own official installer), layered onto the base and content-keyed so identical (base, engine set) builds share one tag. `run`/`map`/`weave` build the image automatically when it's absent.

Antigravity is the one engine where `runtime: container` is not just the recommended isolation — it is the *only* one available. It has no config-home environment variable at all, so `workspace: worktree` on `runtime: host` has nothing to point at; ctxloom refuses that combination as a fatal finding (escapable with `--degraded`, which then runs it on your shared, un-isolated global antigravity config) rather than silently reporting the agent as isolated when it isn't. Containerizing it works — its CLI installs into the composed image like any other engine — but ctxloom does not yet know how to authenticate it inside the container (no scoped env passthrough, no credential mount), so a containerized antigravity agent still needs to log in itself once the container starts.

You control the base, in this order (first one present wins):

1. `--base-image` overlays ctxloom onto an image that already ships the client CLI (skips the install entirely; single-engine).
2. `isolation_base_containerfile` / `--base-containerfile` builds the base from your own Containerfile.
3. **Your project's own `.devcontainer/devcontainer.json`** (or `.devcontainer.json`) is auto-detected as the base — "an isolated agent should run in the environment you develop in". Set `isolation_devcontainer_base: false` (or pass `--no-devcontainer-base` to `container build`) to opt out. `image:` and `build:` shapes are supported; `dockerComposeFile` needs `isolation_devcontainer_service` (or the devcontainer.json's own `service` key) to pick one service, since a multi-service compose project doesn't map to one agent container. Declared `features` are **not** honored (ctxloom does not depend on the devcontainer CLI) — a loud warning names what's skipped, and the build still proceeds from `image`/`build`.
4. The embedded default base (distro plus the coding-agent tool layer — git, ripgrep, curl, certs, jq).

An explicit base always beats auto-detection, and a devcontainer or user base that turns out unbuildable is a **fatal finding**, never a silent fallback to the default — the whole point is running in the environment you actually develop in, not a quietly different one.

`isolation_engines` selects which engine fragments compose into the image (default: every engine with a known official installer — "one instance can run any engine"); trim it to shrink the image. `ctxloom container scaffold` still writes an editable copy of the embedded default base and wires it into `isolation_base_containerfile` when you want to hand-edit the base itself.

`isolation_images` in config names fully user-provided images that run as-is and are never built. An override must honor the **identity contract**: it runs the ctxloom identity-remap entrypoint (base it on a ctxloom-built agent image, or install `ctxloom-entrypoint` as its `ENTRYPOINT`) and bakes no `USER` — otherwise the container would start with the image's own identity and root-own the files it writes into your mounted project. A violating image is a fatal startup finding; `--degraded` launches it anyway with the image's own identity.

`ctxloom container check` diagnoses the environment before you commit to containerized agents: whether this process is itself inside a container, which runtime (docker/podman) is reachable, whether the image exists, and whether the runtime's daemon shares your filesystem — the probe that catches docker-outside-of-docker setups where bind mounts silently resolve against the wrong filesystem. Run it inside a dev container to learn whether to enable docker-in-docker or keep agents on `runtime: host`.

### Tooling declarations

Bundles can declare the tools their content needs inside the agent image (a `tooling` command). `ctxloom tooling` collects the declarations from **trusted** bundles and emits them with instructions for your AI: propose the base-Containerfile additions as a diff, get your explicit approval per change, then rebuild. Nothing is applied automatically on pull or sync.

## Agents vs profiles

| | Profile | Agent |
|---|---------|-------|
| Defines | Context (fragments, commands, MCP servers, variables) | Engine + profiles + runtime |
| Shipped in bundles | Yes (`<bundle>#profiles/<name>`) | Never — local only |
| Used by | `run -p`, `map`/`weave -p`, agents | `run --agent`, `map`/`weave --agents`, `acp --agent` |
| Engine choice | Optional `llm:` preference | Explicit `engine:` binding (overrides the profiles') |

A bare `-p` profile member in `map`/`weave` is effectively an anonymous default-engine agent — reach for named agents when you want a specific engine per role, a containerized runtime, or reusable role names.
