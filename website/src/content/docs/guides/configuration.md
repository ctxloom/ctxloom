---
title: "Configuration"
---

Every `ctxloom run -f go-development -f testing-patterns -p backend-developer` is a flag set you'd otherwise have to remember and retype each session. Put the same choices in `.ctxloom/config.yaml` instead and commit it: everyone on the project runs plain `ctxloom run` and gets the same fragments and profile, without needing to know which flags this repo requires.

ctxloom's configuration lives in YAML files under the `.ctxloom/` directory.

## Directory Structure

```
.ctxloom/
├── config.yaml              # Main configuration
├── remotes.yaml             # Remote registry (and custom forges)
├── lock.yaml                # Dependency lockfile
├── approvals/               # Review decisions, one countersignature per approve/reject
├── allowed_signers          # Publisher keys this project trusts
├── profiles/                # Profile YAML files
│   └── developer.yaml
├── content/                 # Project-authored content, committed with the project
├── cache/                   # Fetched and generated state (gitignored)
│   ├── bundles/             # Local + pulled bundle YAML files
│   ├── repos/               # Remote git clones
│   └── context/             # Assembled context files
├── state/                   # Local-only state nothing can rebuild (gitignored)
└── sessions/                # This machine's distilled session records
```

Every signature and approval here is an SSH signature (the sshsig format
`ssh-keygen -Y sign` writes), checked against an OpenSSH `allowed_signers`
file. ctxloom does not use GPG/PGP keys, keyservers or any other signing scheme.

Agent bindings live under the `agents:` key of `config.yaml` and nowhere else;
ctxloom does not read a `.ctxloom/agents/` directory. Each session's own state
(its engine home, transcript and artifacts) lives outside the project, under
`~/.ctxloom/sessions/<harp>`.

## Config Hierarchy

`config.yaml` is layered. Lowest precedence first:

1. **Home**: `~/.ctxloom/config.yaml`
2. **Project**: `.ctxloom/config.yaml` at the git repository root
3. **Environment**: `CTXLOOM_CONFIG_<PATH>` variables
4. **Flag**: `--config-set <dotted.path>=<value>`, for one invocation

The two files are deep-merged (lists replace rather than append). Each key also
has a scope that decides which layers may set it at all. A fact about this
machine, such as `llm.configs.<label>.binary_path`, `editor`, the top-level
`runtime`, or the `isolation_images`/`isolation_engines`/`isolation_devcontainer_*`
keys, is dropped with a warning when it appears in the committed project file;
put it in your home config. A per-project grant such as `permissions` goes the
other way (see [Permissions](#permissions)).

## config.yaml Reference

The current schema is version 6. The canonical commented example ships as `resources/example-config.yaml` in the repo; `ctxloom config create` scaffolds one.

:::note[Unknown keys are rejected]
`config.yaml` is validated against its schema on load. A key the current schema doesn't recognize, including a retired one such as the old top-level `profiles:` or `hooks:` block, fails startup with a diagnostic naming the key and, where one exists, its replacement. Pass `--degraded` or set `CTXLOOM_DEGRADED=1` to downgrade this to a warning and continue.
:::

```yaml
version: 6

# Language model configuration.
# `llm.configs` is a registry of arbitrarily-labeled backend configs — the
# backend is determined ONLY by each entry's `type`. `llm.defaults` maps a
# role to the label that plays it. ctxloom ships a built-in primary
# (claude-code) and fast config (claude-code on haiku), so this block is
# only needed to change models, binaries, or role pairings.
llm:
  configs:
    big:   { type: claude-code, model: claude-opus-4-8 }
    quick: { type: claude-code, model: claude-haiku-4-5-20251001 }
  defaults:
    primary: big      # coding/interactive role → label
    fast: quick       # compression role (distill, compaction) → label

# Behavioral settings
config:
  use_distilled: true         # prefer distilled fragment versions (default true)
  statusline: true            # let ctxloom manage the HUD statusline
  essence_max_chars: 10000    # character budget for a distilled session essence
  sign:                       # publisher-signing defaults for `bundle push`
    default: false             # sign every push unless --no-sign (default false)
    key: ""                    # SSH key path or SHA256:... fingerprint (default: auto-discover; home config only)

# Editor (fallback: VISUAL env → EDITOR env → nano). Home config only.
editor:
  command: "vim"
  args: []

# The default agent: what a bare `ctxloom run` (no --agent/-p/-f/-t) binds.
# Its composed profiles become the context, its engine/runtime/permissions
# the transport — this is the key that makes plain `ctxloom run` resolve any
# context at all. Names an entry under agents: below.
default_agent: dev

# Agents: local engine↔profile bindings (see the Agents concept page)
agents:
  dev:
    llm: claude-code          # an llm.configs label (engine + model)
    profiles: [developer]
    runtime: container-rootless # optional; host|container-rootless|container-rootful
    permissions: acceptEdits  # optional; default|acceptEdits|plan|bypass
    escalation: []            # optional; ordered approval-request ladder, overrides the permissions-derived default

# Profiles are files, one per profile, under .ctxloom/profiles/<name>.yaml.
# config.yaml has no profiles: key.

# The default permission posture for agents run IN THIS DIRECTORY.
# Only ever read from this project file — see "Permissions" below.
permissions: acceptEdits      # default|acceptEdits|plan|bypass

# Isolation defaults
workspace: none               # session workspace axis: none|worktree
runtime: host                 # agent runtime axis: host|container-rootless|container-rootful (home config only)

# Container-image overrides for containerized agents
isolation_base_containerfile: .ctxloom/base.Containerfile   # your base stage
isolation_devcontainer_base: true      # auto-detect .devcontainer/devcontainer.json as the base (default true; home config only)
isolation_devcontainer_service: app    # compose service to use as the base, if devcontainer.json declares dockerComposeFile (home config only)
isolation_engines: [claude-code]      # trim the composed engine set (default: every known engine; home config only)
isolation_images:             # fully user-provided images, run as-is (home config only)
  claude-code: my-registry/claude-agent:latest

# Sync configuration
sync:
  auto_sync: true             # sync referenced remotes on startup (default true)

# Hooks are NOT configured here. A profile or bundle declares them under its
# own hooks: key (see "Hooks" below).

# MCP servers are NOT configured here. They ship in bundles: a bundle's
# `mcp:` block declares a server, and composing that bundle registers it.
# ctxloom's own server is declared by its companion loadout and served by the
# running session, so it is on by default. Withhold one with a profile's
# `exclude_mcp: [<name>]`.
```

## LLMs

Registered LLM backends:

| Backend | CLI | Description |
|---------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude (default) |

Each backend launches the vendor's own CLI as a child process — ctxloom holds no model API
client of its own, so the backend's binary must be installed and on `PATH` before you can run
it. See [Installation → Prerequisites](/getting-started/installation/#running-an-ai-engine) for
the binary each backend needs.

A **config label** is an arbitrary name for a fully-specified backend config; the backend is chosen by the entry's `type`. Two labels can point at the same backend with different models (e.g. a `big` and a `quick` claude-code). Set the interactive default with `llm.defaults.primary` (or per-run with `--llm <label>`), and the compression role with `llm.defaults.fast`:

```yaml
llm:
  configs:
    claude-code:
      type: claude-code
      model: "claude-opus-4-8"
      binary_path: "/path/to/bin"   # optional; home config only
      args: []                      # extra CLI arguments
  defaults:
    primary: claude-code
```

A config entry carries no credentials or environment. The engine reads those
from the environment ctxloom runs in, so export them in that shell; an `env:`
key on an entry is refused at load.

`ctxloom llm list` shows the available backends; `ctxloom llm default <label>` sets the primary.

## Defaults

| Setting | Default | Description |
|---------|---------|-------------|
| `config.use_distilled` | `true` | Prefer distilled content |
| `config.statusline` | `true` | Manage the ctxloom HUD statusline (set `false` to keep your own) |
| `sync.auto_sync` | `true` | Sync remotes on startup |
| `llm.defaults.primary` | `claude-code` | Default LLM backend |
| `workspace` | `none` | Session workspace axis default |
| `runtime` | `host` | Agent runtime axis default |
| `permissions` | engine's own | Default permission posture for this project directory |

## Permissions

Approving every edit gets old fast in a scratch repo, and approving nothing at
all is the wrong answer in the one that ships. `permissions:` lets you settle
that question **once per project directory**:

```yaml
# .ctxloom/config.yaml — in the project you want it to apply to
permissions: bypass       # default | acceptEdits | plan | bypass
```

Every agent launched in this directory now starts at that posture, with no
flag to remember and nothing to re-type. A scratch repo can be `bypass`; the
repo that deploys can be `plan`; neither one learns anything about the other.

**This key is only honored from the project's own `.ctxloom/config.yaml`.**
The same line in your `~/.ctxloom/config.yaml`, or in
`CTXLOOM_CONFIG_PERMISSIONS`, is dropped with a warning and never applied.
That is the entire point of it. The grant is consent for one directory you
chose deliberately; a home-wide or environment-wide version would silently
re-grant every project on the machine what you meant for one of them — and an
agent that can run `bash` can write an environment variable, which would let
it widen its own successors.

The postures:

| Value | What the engine does |
|-------|----------------------|
| `default` | Prompts you for each gated call |
| `acceptEdits` | Auto-accepts file edits, prompts for the rest |
| `plan` | Read-only: it may inspect, not mutate |
| `bypass` | No in-engine prompting at all |

Anything more specific wins. The full order, nearest first:

```
run --permissions  >  the agent binding's `permissions`
                   >  the engine label's `permissions`
                   >  this project default
                   >  the engine's own built-in default
```

So a `reviewer` agent declaring `permissions: plan` stays read-only in a
project whose default is `bypass` — a project default can never widen a
posture you wrote down somewhere more specific. It is precedence, not
"strictest wins": a binding may equally declare a *wider* posture than the
project default, exactly as it can today against the built-in one.

Declaring the project default also settles what would otherwise be an engine's
own choice — claude-code runs at `bypass` on the host when nobody has said
otherwise, so `permissions: plan` in a claude-code project is the difference
between read-only and unrestricted.

:::caution
`bypass` means the engine asks nothing before running commands or writing
files. Its blast radius is whatever contains the process — a container, or
nothing at all on the bare host. Pair a permissive project default with
`runtime: container-rootless` on the agent binding (below) if the directory is
not one you would hand a stranger a shell in.
:::

## Agents and Isolation

The `agents:`, `workspace:`, `runtime:`, `isolation_images:`,
`isolation_base_containerfile:`, `isolation_devcontainer_base:`,
`isolation_devcontainer_service:`, and `isolation_engines:` keys configure
local agent bindings and where they execute. See
[Agents & Isolation](/concepts/agents/) for the model, and prefer
`ctxloom agent create` / `ctxloom agent edit` over hand-editing the `agents:` key.

## Hooks

Hook types available:

| Hook | When |
|------|------|
| `pre_tool` | Before tool execution |
| `post_tool` | After tool execution |
| `session_start` | Session initialization |
| `session_end` | Session cleanup |
| `turn_end` | The agent finished a turn (once per response) |
| `turn_start` | A prompt was submitted, before the agent acts on it (once per turn) |
| `pre_shell` | Before shell execution |
| `post_file_edit` | After file edit |

Hooks are declared in a profile (or a bundle), not in `config.yaml`. A profile's
hooks fire in every session that composes it; `ctxloom manage hooks list` shows
the merged order and where each hook came from.

```yaml
# .ctxloom/profiles/developer.yaml
hooks:
  unified:
    session_start:
      - matcher: ".*"           # Regex pattern
        command: "echo hello"   # Shell command
        type: "command"         # command, prompt, or agent
        timeout: 30             # Seconds
        async: false            # Run in background
  ext:                          # engine-specific hooks, by native event name
    claude-code:
      EventName: []
```

## Claude Code Integration

A `ctxloom run` session hands the assembled context to Claude Code as an appended
system prompt file inside the session's own home, so neither `CLAUDE.md` nor
`.claude/settings.json` in your project is touched. For a Claude Code you start
directly, `ctxloom manage hooks install` writes a SessionStart hook that reads
`.ctxloom/cache/context/[hash].md`. See
[Hooks and Context Injection](/guides/hooks) for both paths.

## Sync Configuration

```yaml
sync:
  auto_sync: true      # Sync on startup (the only sync setting)
```

### Lockfile

The `lock.yaml` records the resolved remote items for reproducible pulls. It is
updated automatically whenever you pull or upgrade:

```bash
ctxloom deps pull        # Fetch referenced content and update lock.yaml
```

## Memory Configuration

Session memory is always enabled. The compaction/distillation model is the
**fast role** (`llm.defaults.fast`), and the size of a distilled session
essence is a behavioral setting:

```yaml
llm:
  defaults:
    fast: quick              # config label used for distillation
config:
  essence_max_chars: 10000   # character budget for a distilled session essence
```

See [Session Memory Guide](/getting-started/memory) for usage details.
