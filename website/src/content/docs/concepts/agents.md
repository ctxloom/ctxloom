---
title: "Agents & Isolation"
---

Your `developer` profile is right for a quick question on your laptop and wrong for a long unattended run, the kind you'd rather box up in a container so it can't touch anything outside the project. Editing the profile itself every time you want a different engine or runtime defeats the point of having a reusable profile at all.

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
```

An agent names:

- **`engine`** — the LLM config label or backend to run. It overrides the constituent profiles' own `llm:`; omit it to use the project default.
- **`profiles`** — one or more profiles that compose into a single assembled context.
- **`runtime`** (optional) — where the engine process executes: `host` or `container`. Omit to inherit the project's `runtime:` default.

Agents live solely in your `.ctxloom` — under the `agents:` key of `config.yaml` or as `.ctxloom/agents/<name>.yaml` files. They are **never shipped in bundles or remotes**: bundles distribute portable context, but the engine choice (which costs money and holds your credentials) always stays yours.

## Managing agents

```bash
ctxloom agent set finder --engine claude-fast --profiles finder
ctxloom agent set dev --engine claude-code --profiles default,go-developer --runtime container
ctxloom agent set reviewer --profiles cr-correctness-go   # default engine
ctxloom agent list
ctxloom agent show dev
ctxloom agent remove reviewer
```

Re-running `agent set` with the same name updates the binding.

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

Agents with `runtime: container` run their engine inside a per-backend **agent image**:

```bash
ctxloom container check          # can containerized agents launch here?
ctxloom container build          # build/refresh the image for the default backend
ctxloom container scaffold       # materialize an editable base Containerfile
```

Images build in two stages: a shared **base** (distro plus the coding-agent tool layer — git, ripgrep, curl, certs, jq) and the engine's **agent stage** (the client CLI plus the running ctxloom binary) layered on top. `run`/`map`/`weave` build the image automatically when it's absent.

You control the base:

- `ctxloom container scaffold` writes the default base Containerfile to `.ctxloom/base.Containerfile` and sets `isolation_base_containerfile`, so your tools, certs, and mirrors layer under every locally-built agent image.
- `--base-image` overlays ctxloom onto an image that already ships the client CLI.
- `isolation_images` in config names fully user-provided images that run as-is and are never built. An override must honor the **identity contract**: it runs the ctxloom identity-remap entrypoint (base it on a ctxloom-built agent image, or install `ctxloom-entrypoint` as its `ENTRYPOINT`) and bakes no `USER` — otherwise the container would start with the image's own identity and root-own the files it writes into your mounted project. A violating image is a fatal startup finding; `--degraded` launches it anyway with the image's own identity.

`ctxloom container check` diagnoses the environment before you commit to containerized agents: whether this process is itself inside a container, which runtime (docker/podman) is reachable, whether the image exists, and whether the runtime's daemon shares your filesystem — the probe that catches docker-outside-of-docker setups where bind mounts silently resolve against the wrong filesystem. Run it inside a dev container to learn whether to enable docker-in-docker or keep agents on `runtime: host`.

### Tooling declarations

Bundles can declare the tools their content needs inside the agent image (a `tooling` skill). `ctxloom tooling` collects the declarations from **trusted** bundles and emits them with instructions for your AI: propose the base-Containerfile additions as a diff, get your explicit approval per change, then rebuild. Nothing is applied automatically on pull or sync.

## Agents vs profiles

| | Profile | Agent |
|---|---------|-------|
| Defines | Context (fragments, skills, MCP servers, variables) | Engine + profiles + runtime |
| Shipped in bundles | Yes (`<bundle>#profiles/<name>`) | Never — local only |
| Used by | `run -p`, `map`/`weave -p`, agents | `run --agent`, `map`/`weave --agents`, `acp --agent` |
| Engine choice | Optional `llm:` preference | Explicit `engine:` binding (overrides the profiles') |

A bare `-p` profile member in `map`/`weave` is effectively an anonymous default-engine agent — reach for named agents when you want a specific engine per role, a containerized runtime, or reusable role names.
