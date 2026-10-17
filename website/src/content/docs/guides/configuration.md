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

The two files are deep-merged (lists replace rather than append), and every
layer may set every key: a higher layer wins. An agent binding is the
exception to the deep merge: the highest layer that names an agent supplies its
whole binding. The `companions` list is the exception to "lists replace": a
project's names are added to the ones your home config registers, and a project
cannot remove a home registration. A command that writes configuration writes only the file it
targets, so a value from your home config or the environment is never copied
into the project file.

To see the result of that merge, run `ctxloom config show`. Give it a top-level
section name (`ctxloom config show llm`) to print only that section; an unknown
name is refused with the list of sections that exist. `--raw` leaves out the
shipped default engine registry, so only what your configuration sets is shown.

## config.yaml Reference

The file declares its format generation as `schema_version`. A file declaring none (`version` is not a spelling of it), or one older than this ctxloom reads, is refused; a file written for a newer ctxloom is refused, naming both numbers. What a load changes in memory (such as resolving an `<alias>/…` ref) is left unchanged on disk; pass `--write-upgrades` to any command to persist it, keeping the previous file as `config.yaml.bak`. The canonical commented example ships as `resources/example-config.yaml` in the repo; `ctxloom config create` scaffolds one.

:::note[A config that breaks its schema is rejected]
`config.yaml` is validated against its schema on load, and a violation fails every command that reads the config. A key the current schema doesn't recognize, including a retired one such as the old top-level `profiles:` or `hooks:` block, is named with the nearest known key and the keys its section does know. A value the schema refuses — the wrong type, or outside its allowed values — is named by file and dotted key path, with the type the schema expected (for example, `session_reap_age` holds a number, expected string) or the rule it broke. Pass `--degraded` or set `CTXLOOM_DEGRADED=1` to downgrade these to warnings and continue on the config as loaded.
:::

```yaml
schema_version: 8

# Language model configuration.
# `llm.configs` is a registry of arbitrarily-labeled backend configs — the
# backend is determined ONLY by each entry's `type`. `llm.defaults` maps a
# role to the label that plays it. ctxloom ships a built-in primary
# (claude-code) and fast config (claude-code on haiku), so this block is
# only needed to change models, binaries, or role pairings.
llm:
  configs:
    big:   { type: claude-code, model: opus }
    quick: { type: claude-code, model: haiku }
  defaults:
    primary: big      # coding/interactive role → label
    fast: quick       # compression role (distill) → label

# Behavioral settings
config:
  use_distilled: true         # prefer distilled fragment versions (default true)
  statusline: true            # let ctxloom manage the HUD statusline

# Editor (fallback: VISUAL env → EDITOR env → nano).
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
    permissions:              # optional; see the Agents concept page for every field
      claude-code:            # the engine's own keys, in a block named for it
        mode: acceptEdits

# Profiles are the project bundle's items, one per file, under
# .ctxloom/content/bundles/v2/project/profiles/<name>.yaml.
# config.yaml has no profiles: key.

# The engine-neutral permission defaults for agents run IN THIS DIRECTORY.
# Only ever read from this project file — see "Permissions" below.
permissions:
  sandbox: workspace-write    # read-only|workspace-write|full
  approver: human             # human|none|reviewer

# Isolation defaults
workspace: none               # session workspace axis: none|worktree
runtime: host                 # agent runtime axis: host|container-rootless|container-rootful

# Container-image overrides for containerized agents
isolation_base: devcontainer           # agent image base: ctxloom | devcontainer | <image ref> (unset: the devcontainer when present)
isolation_devcontainer_service: app    # compose service to use as the base, if devcontainer.json declares dockerComposeFile
isolation_engines: [claude-code]      # trim the composed engine set (default: every known engine)
isolation_images:             # fully user-provided images, run as-is
  claude-code: my-registry/claude-agent:latest
isolation_memory_limit: 8g    # per-container memory cap (unset: half host memory, 2 GiB floor; 0|unlimited: none)
isolation_pids_limit: 4096    # per-container process cap (unset: 4096; 0|unlimited: none)

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
| `opencode` | [opencode](https://opencode.ai) | opencode on one of its built-in providers (e.g. the keyless `opencode/big-pickle`) or against an OpenAI-compatible endpoint (e.g. a LAN llama-server) |

Each backend launches the vendor's own CLI as a child process — ctxloom holds no model API
client of its own, so the backend's binary must be installed and on `PATH` before you can run
it. See [Installation → Prerequisites](/getting-started/installation/#running-an-ai-engine) for
the binary each backend needs.

A **config label** is an arbitrary name for a fully-specified backend config; the backend is chosen by the entry's `type`. Two labels can point at the same backend with different models (e.g. a `big` and a `quick` claude-code). Set the interactive default with `llm.defaults.primary` (or per-run with `--llm <label>`), and the fallback for distillation and trigger triage with `llm.defaults.fast` (used only when no `distiller` / `triage` agent is configured):

```yaml
llm:
  configs:
    claude-code:
      type: claude-code
      model: "opus"                 # a claude CLI alias, or a full model id to pin one
      binary_path: "/path/to/bin"   # optional
      args: []                      # extra CLI arguments
      context_window: 1000000       # optional; only opencode reads it
  defaults:
    primary: claude-code
```

`context_window` is the model's context window in tokens. Any label may carry it, whatever its
`type`, so a label can move between engines, but only opencode reads it: opencode cannot
discover an endpoint's window and never compacts without one (see the opencode label below).

`binary_path` and `args` are neutral too: any label may carry them. `binary_path` is the binary
the launch spawns in place of the engine's own (`claude`, `opencode`), and the one whose version
ctxloom checks against the engine's floor and records on the session. `args` are placed ahead of
everything ctxloom composes on that engine's command line. Both are checked when the label is
launched: a `binary_path` that is not a string, or `args` that is not a list of strings, refuses
the launch naming the label (`label "big": config: a label value is malformed: args must be a
list of strings, got string`). It is never read as absent, so a typo cannot quietly launch the
stock binary with no arguments. Loading the config only warns about such a value; the launch is
where it fails.

An `opencode` label drives the opencode CLI. It requires `model`. Without a `provider`
block, `model` names one of opencode's built-in providers and is passed through as written,
and no key is needed:

```yaml
llm:
  configs:
    free:
      type: opencode
      model: "opencode/big-pickle"  # a free hosted model; no key
      permissions:
        approver: none
```

With a `provider` block it drives opencode against that OpenAI-compatible endpoint, and also
requires `context_window` (opencode cannot discover an endpoint's window and never compacts
without it):

```yaml
llm:
  configs:
    local:
      type: opencode
      model: "qwen3.8-27b"          # the id the endpoint serves (llama-server's --alias)
      context_window: 131072        # llama-server: --ctx-size divided by --parallel
      provider:
        base_url: "http://192.168.1.20:8081/v1"
        output_limit: 8192          # reserved out of context_window when opencode decides to compact
        header_timeout_ms: 0        # optional; 0 disables it (a single-slot server queues requests)
        chunk_timeout_ms: 300000    # optional
      model_options:                # optional; passed verbatim into every request body
        chat_template_kwargs: {enable_thinking: false}
      snapshot: true                # optional; default on interactive (the TUI's /undo), off structured
      title_generation: false       # optional; default off
      share: disabled               # optional; manual | auto | disabled (default)
      permissions:
        mode: build                 # build | plan | auto
        approver: none              # required: opencode run rejects every ask itself
```

The same label can be written from the CLI: `--set` takes any key the label's engine accepts, by
dotted path, and `--home` writes `~/.ctxloom/config.yaml`, the right file for an endpoint on
your own machine or LAN, which every project reads:

```bash
ctxloom llm create local --home --type opencode --model qwen3.8-27b \
  --set context_window=131072 \
  --set provider.base_url=http://192.168.1.20:8081/v1 \
  --set provider.output_limit=8192 \
  --set permissions.approver=none
ctxloom llm edit local --home --set provider.header_timeout_ms=0
```

The whole entry is checked against the config schema before it is written, so an unknown key,
a value of the wrong type, or a `provider` block missing `output_limit` is refused and nothing
changes. An empty value (`--set share=`) removes a key.

The endpoint's key never goes in the config: export `CTXLOOM_OPENCODE_API_KEY` and the run's
credentials carry it. A built-in provider label needs no key; when the variable is not
exported, the run starts keyless. See the [config reference](/reference/config/) for every key.

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
| `permissions` | engine's own | Engine-neutral permission defaults for this project directory |

## Permissions

Some permission settings mean the same thing whatever engine runs: who
answers a request the posture leaves open, how long it waits, and what the
engine's own commands may touch. `permissions:` lets you settle those **once
per project directory**:

```yaml
# .ctxloom/config.yaml — in the project you want it to apply to
permissions:
  approver: none              # human | none | reviewer
  approval_timeout: 20m
  sandbox: workspace-write    # read-only | workspace-write | full
  network: false
```

Every agent launched in this directory takes these unless something more
specific declares its own. A scratch repo can run `sandbox: full`; the repo
that deploys can hold its engines to `workspace-write`; neither one learns
anything about the other.

It layers like every other key: set in `~/.ctxloom/config.yaml` it applies to
every project that does not declare its own.

| Field | Values |
|-------|--------|
| `approver` | `human` (the default: you answer), `none` (nobody: the request is denied), `reviewer` (the engine's own classifier, where the engine has one) |
| `approval_timeout` | how long a request waits before it is denied (default 15m, at most 60m) |
| `sandbox` | `read-only`, `workspace-write` (the working tree only) or `full`; undeclared, the engine's default. A value the engine cannot enforce where the agent runs is refused |
| `network` | whether sandboxed commands may reach the network |

An engine's mode and its rules (`allow` / `deny` / `ask`) are that engine's
grammar, so they are declared where the engine is known: in an agent
binding's block for that engine, or flat on an llm label. One written here is
refused at load, naming where it goes. See the Agents concept page.

Anything more specific wins, key by key. The order, nearest first:

```
run --permissions  >  the agent binding's `permissions`   (the mode only)
                   >  the engine label's `permissions`
                   >  this project default                  (the neutral fields only)
                   >  the engine's own default
```

So an agent declaring `sandbox: full` runs unsandboxed in a project whose
default is `workspace-write` — it is precedence, not "strictest wins", and a
binding may equally declare something narrower.

:::caution
A posture that asks nothing (claude-code's `bypass`) and `sandbox: full`
together bound nothing but whatever contains the process — a container, or
nothing at all on the bare host. Pair a permissive agent with
`runtime: container-rootless` on its binding (below) if the directory is not
one you would hand a stranger a shell in.
:::

## Agents and Isolation

The `agents:`, `workspace:`, `runtime:`, `isolation_images:`,
`isolation_base:`, `isolation_devcontainer_service:`, `isolation_engines:`,
`isolation_memory_limit:` and `isolation_pids_limit:` keys configure local agent bindings,
where they execute, and what a containerized agent may use. See
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
# .ctxloom/content/bundles/v2/project/profiles/developer.yaml
hooks:
  unified:
    pre_tool:
      - tool: shell             # tool class: shell, file_edit or skill
        command: "echo hello"   # Shell command
        type: "command"         # command, prompt, or agent
        timeout: 30             # Seconds
        async: false            # Run in background
  ext:                          # engine-specific hooks, by native event name
    claude-code:
      EventName: []
```

`tool:` narrows a tool event to a class of tool, which each engine maps to its
own tool names (claude: `shell` is `Bash|PowerShell`, `file_edit` is
`Edit|Write|MultiEdit|NotebookEdit`). A profile hook may instead give
`matcher:`, a regex over one engine's tool names, which wins when both
are set; a bundle hook may only use `tool:`, and a `matcher:` in a bundle is
refused when the bundle is read.

A `pre_tool` or `pre_shell` command hook reads the same input on every engine:
Claude Code's `PreToolUse` JSON on stdin (`tool_name`, `tool_input`, `cwd`,
`session_id`, ...). On opencode, which has no hook commands of its own,
ctxloom's plugin runs the hook and writes that input itself, with opencode's
tools under Claude Code's names and input keys (`bash` is `Bash`, `edit` is
`Edit` with `file_path`, `old_string`, `new_string`; `apply_patch`, which Claude
Code lacks, keeps its name; an MCP tool passes as opencode names it). A
`matcher:` on opencode is tested against those Claude Code names, so one
matcher works on both engines; `shell` is `Bash` and `file_edit` is
`Edit|Write|apply_patch`. There the one answer that blocks the call is a deny
with a non-empty reason, written on stdout as Claude Code's
`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"..."}}`;
the reason reaches the model. Any other answer, a non-zero exit (2 included) or
a timeout lets the call through, so write a deny, not an exit status. A hook written for Claude Code's `PreToolUse`, such as
`ltk evaluate`, therefore guards opencode unchanged.

## Claude Code Integration

A `ctxloom run` session hands the assembled context to Claude Code as an appended
system prompt file inside the session's own home, so neither `CLAUDE.md` nor
`.claude/settings.json` in your project is touched. A Claude Code you start
directly gets ctxloom's context only from files `ctxloom materialize` wrote into
the project (its context section in `CLAUDE.md`); the hooks `ctxloom materialize`
writes there include a SessionStart hook that never carries the context. See
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

## Session Configuration

ctxloom does not compact sessions; your engine does. A session's transcript is
its record, and every command that destroys transcripts keeps one until the
session ended more than `session_transcript_retention` ago (default `30d`, in
the `clean --older-than` grammar), or at any age while the session is marked
keep (`ctxloom session keep <harp>`):

```yaml
session_transcript_retention: 60d   # keep transcripts for 60 days after a session ends
```

Distillation (prompt compression) runs as the agent named `distiller` — its
engine, environment and runtime — falling back to the **fast role**
(`llm.defaults.fast`) with a warning when no such agent is configured. Either
way the session is offered no tool and no MCP server, so the agent's
`permissions:` block does not apply to it.

See [Session History](/getting-started/memory) for usage details.
