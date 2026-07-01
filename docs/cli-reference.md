# ctxloom CLI Reference

Complete reference for all ctxloom commands and options.

## Quick Reference

```bash
ctxloom run -p <profile> "prompt"     # Run with profile
ctxloom search -t <tag>               # Search by tag
ctxloom fragment list                 # List fragments
ctxloom remote pull                   # Pull referenced bundles/profiles
```

## Commands

### Workflow Commands

#### `ctxloom run`

Assemble context and run AI.

```bash
ctxloom run [flags] [prompt...]
```

**Flags:**
| Flag | Short | Description |
|------|-------|-------------|
| `--fragment` | `-f` | Add fragment by reference (repeatable) |
| `--profile` | `-p` | Load a predefined profile |
| `--tag` | `-t` | Include fragments with tag (repeatable) |
| `--prompt` | | Custom prompt text |
| `--saved-prompt` | | Load saved prompt template |
| `--llm` | `-l` | Config label to use (e.g. claude-code, claude-fast, gemini-code, gemini-fast); overrides the configured default |
| `--dry-run` | `-n` | Preview context without running |
| `--print` | | Run non-interactively (oneshot) and print the response |
| `--verbose` | `-v` | Increase verbosity (-v, -vv, -vvv) |

With `--print` and no prompt given, the prompt is read from **stdin** when stdin
is piped. This makes `run --print` a universal reducer/synthesizer over any input
— ctxloom-produced or not:

```bash
ctxloom map -p code-review/security -p code-review/perf "<diff>" \
  | ctxloom run -p code-review/synthesis --print     # synthesize the parts
cat findings-from-some-tool.txt | ctxloom run -p code-review/synthesis --print
```

A profile may declare its own preferred LLM (`llm:`); `--llm`/`-l` overrides it.

**Examples:**
```bash
ctxloom run -p developer "explain this code"
ctxloom run -f core#fragments/tdd -t golang "review PR"
ctxloom run -n  # Preview what context would be sent
```

#### `ctxloom map`

Run multiple profiles in parallel over one shared task (the fan-out half of the
`weave` ensemble primitive). Each `-p` profile runs as its own oneshot agent with
its own context and LLM; outputs are emitted as labeled blocks. Bounded by
`--concurrency` and fault-tolerant (a failed member is reported inline, others
continue).

```bash
ctxloom map [flags] [task...]
```

**Flags:**
| Flag | Short | Description |
|------|-------|-------------|
| `--profile` | `-p` | Profile to run as a parallel member (repeatable) |
| `--llm` | `-l` | Override the LLM for every member (else each profile's own `llm:`) |
| `--concurrency` | | Max members to run at once (default 4) |
| `--save-parts` | | Directory to write each member's raw output into |

The task is taken from the arguments, or from stdin when no arguments are given.
The labeled output is meant to be read, saved (`--save-parts`), or piped into a
synthesizer:

**Examples:**
```bash
git diff | ctxloom map -p code-review/security -p code-review/perf
ctxloom map -p a -p b -p c --concurrency 3 "review this change" \
  | ctxloom run -p synthesis --print
```

#### `ctxloom weave`

Fan a task across profiles in parallel, then synthesize the results — the
composite of `map` + a synthesis `run`, but in-process (no shell) so it works
identically on every platform. Each `-p` member runs as its own agent on its own
`llm:`; the `-s` synthesizer runs on its own (typically high-power) `llm:`.

```bash
ctxloom weave [flags] [task...]
```

**Flags:**
| Flag | Short | Description |
|------|-------|-------------|
| `--profile` | `-p` | Member profile to run in parallel (repeatable) |
| `--synthesize` | `-s` | Synthesis profile that combines member outputs |
| `--llm` | `-l` | Override the LLM for every member (synthesizer keeps its own `llm:`) |
| `--concurrency` | | Max members to run at once (default 4) |
| `--part` | | Inject `NAME=FILE` as a part to synthesize (repeatable) |
| `--parts-from` | | Inject every file in a directory as a part |
| `--save-parts` | | Directory to write each member's raw output into |
| `--no-synthesize` | | Emit the labeled parts only; skip synthesis |

`weave` is equivalent to `ctxloom map -p A -p B "task" | ctxloom run -p SYNTH
--print`, but portable and single-invocation. The `--part`/`--parts-from` flags
let you synthesize **non-ctxloom outputs** alongside (or instead of) live members.

**Examples:**
```bash
ctxloom weave -p code-review/security -p code-review/performance \
  -s code-review/synthesis "review this diff"
git diff | ctxloom weave -p reviewer/a -p reviewer/b -s synthesis
ctxloom weave -p a -p b -s synth --part legacy=old-report.txt "audit"
ctxloom weave -s synth --parts-from ./collected "merge these findings"
```

#### `ctxloom init`

Initialize a new .ctxloom directory.

```bash
ctxloom init [flags]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--home` | Initialize in ~/.ctxloom instead of current directory |
| `--non-interactive` | Skip interactive prompts |
| `--skip-launch` | Don't auto-launch AI after init |
| `--engine` | Pre-select AI engine (claude-code, gemini, etc.) |

**Examples:**
```bash
ctxloom init                    # Interactive setup
ctxloom init --engine gemini    # Pre-select engine
ctxloom init --home             # Initialize global config
```

---

### Content Commands

#### `ctxloom fragment`

Manage context fragments.

| Subcommand | Description |
|------------|-------------|
| `list` | List all fragments |
| `show <ref>` | Show fragment content |
| `create <bundle> <name>` | Create new fragment |
| `delete <ref>` | Delete fragment |
| `edit <ref>` | Edit fragment in editor |
| `distill <ref>` | Create token-efficient version |
| `search [query]` | Search fragments |
| `push <bundle> [remote]` | Push bundle to remote |

**Reference format:** `bundle#fragments/name`

**Examples:**
```bash
ctxloom fragment list
ctxloom fragment list --bundle core
ctxloom fragment show core#fragments/tdd
ctxloom fragment show core#fragments/tdd --distilled
ctxloom fragment create my-bundle coding-standards
ctxloom fragment edit core#fragments/tdd
ctxloom fragment search -t golang
```

To consume a remote bundle (or a single fragment from it), author a local profile
that references it, then pull:

```bash
ctxloom profile create developer -b ctxloom-default/core
ctxloom profile create developer -b ctxloom-default/core#fragments/tdd
ctxloom remote pull
```

#### `ctxloom skill`

Manage skills. Same subcommands as `fragment`.

**Reference format:** `bundle#skills/name`

**Examples:**
```bash
ctxloom skill list
ctxloom skill show my-bundle#skills/code-review
ctxloom skill create my-bundle review-pr
```

#### `ctxloom profile`

Manage profiles (named fragment collections).

| Subcommand | Description |
|------------|-------------|
| `list` | List all profiles |
| `show <name>` | Show profile details |
| `create <name>` | Create new profile |
| `delete <name>` | Delete profile |
| `edit <name>` | Edit profile YAML |
| `modify <name>` | Modify profile configuration |
| `default [name\|ref]` | Show, set, or clear (`--unset <name>`) the default profile(s) |
| `push <name> [remote]` | Push profile to remote |
| `export <name> <dir>` | Export profile to directory |
| `import <file>` | Import profile from file |

A profile may declare a preferred **LLM** via the `llm:` field (a config label or
backend type). `ctxloom run` uses it unless `--llm`/`-l` overrides; in a `weave`
ensemble each member runs on its own `llm:` and the synthesizer on a high-power one.
Set it with `--llm` on create/modify:

```bash
ctxloom profile create reviewer --bundle code-review-checklists --llm claude-fast
ctxloom profile modify reviewer --llm gemini-code   # change it; --llm "" clears it
```

When `ctxloom run` is invoked with no profile and no configured default, it shows
an interactive picker of installed profiles (skipped when not on a terminal).

**Consuming remote content (reference-only):** you don't "install" remote items.
You author a local profile that *references* remote content, then pull:

- Reference a remote **bundle** (or one fragment) with `-b`/`--bundle`, optionally
  `#fragments/<name>`, then `ctxloom remote pull`.
- Inherit a remote **profile** with `--parent`, then `ctxloom remote pull`.
- To override remote content locally, copy it under `.ctxloom/local/` and
  reference it as `ctxloom:local@bundles/<name>`.

`create`/`modify` accept **bare convenience refs** (e.g.
`-b code-review-base#fragments/conduct`, `--parent developer`) that expand against
the configured default remote into canonical URLs. Full URLs and
`ctxloom:local@...` refs pass through unchanged.

**Default profiles:** defaults are a *list*; each entry may be a local name or a
remote ref. Manage them with `ctxloom profile default`:

```bash
ctxloom profile default                 # show current default(s)
ctxloom profile default developer       # set/add a default profile
ctxloom profile default --unset developer  # clear it
```

**Examples:**
```bash
ctxloom profile list
ctxloom profile show developer
ctxloom profile create backend --parent developer --bundle go-tools
ctxloom profile create reviewer -b code-review-base#fragments/conduct
ctxloom profile modify backend --add-bundle security
ctxloom remote pull                     # fetch referenced bundles/profiles
```

#### `ctxloom search`

Search fragments and skills.

```bash
ctxloom search [query] [flags]
```

**Flags:**
| Flag | Short | Description |
|------|-------|-------------|
| `--tag` | `-t` | Filter by tags (comma-separated, repeatable) |
| `--type` | | Filter by type: `fragment` or `skill` |

**Examples:**
```bash
ctxloom search cache                    # Search by name
ctxloom search -t golang                # Search by tag
ctxloom search -t golang,testing        # Multiple tags
ctxloom search --type fragment cache    # Only fragments
```

---

### Remote Commands

#### `ctxloom remote`

Manage remote repositories.

| Subcommand | Description |
|------------|-------------|
| `list` | List configured remotes |
| `add <name> <url>` | Add remote source (optionally `--forge <label>`) |
| `remove <name>` | Remove remote |
| `default [name]` | Get/set default remote |
| `pull` | Fetch all remote bundles/profiles referenced by local profiles, update the lockfile, and apply hooks |
| `search <query>` | Search across remotes |
| `browse <remote>` | Browse remote contents |
| `discover` | Discover ctxloom repositories |
| `update` | Check for and apply updates |

**URL formats:**
- `user/repo` - GitHub shorthand (expands to `https://github.com/user/repo`)
- `https://github.com/user/repo` - Full GitHub URL
- `https://git.example.com/corp/repo` - Any other git host (GitLab, Gitea, Bitbucket, self-hosted)
- `git@github.com:user/repo.git` - SSH URL (converted to HTTPS)

**Forges:**

A remote binds to a *forge* — the adapter ctxloom uses to read and publish:

- `github` — rich adapter over the GitHub REST API (file/dir reads, ref
  resolution, repo search, PR publish). Serves github.com and GitHub Enterprise
  (the host comes from the remote URL; the API endpoint is derived from it).
  Token: the env var named by the forge's `token_env`, default `GITHUB_TOKEN`.
- `git` — generic adapter that clones over HTTPS/SSH and reads the working copy.
  Works against any git host for consumption (no API search or PR publish). Auth
  is ambient git (credential helper, ssh-agent, `~/.ssh/config`, per-host
  `.gitconfig`).

Without `--forge`, the forge resolves from the URL host: github.com (and the
`owner/repo` shorthand) use `github`; every other host uses `git`. Pass
`--forge <label>` to override with `github`, `git`, or a `forges:` label
configured in `remotes.yaml` (for example a GitHub Enterprise instance with its
own `base_url`/`token_env`).

**Examples:**
```bash
ctxloom remote list
ctxloom remote add personal myuser/ctxloom-profiles
ctxloom remote add corp https://git.example.com/corp/repo --forge git
ctxloom remote add work https://github.mycorp.com/me/ctxloom --forge work-ghe
ctxloom remote default personal
ctxloom remote pull                     # Fetch referenced bundles/profiles, lock, apply hooks
ctxloom remote search golang
ctxloom remote browse ctxloom-default
ctxloom remote discover --min-stars 10
ctxloom remote update                   # Check for updates
```

Locking happens automatically as part of `pull` (and `update`); there is no
separate lock step. To override remote content for local development, copy it
under `.ctxloom/local/` and reference it as `ctxloom:local@bundles/<name>` from a
profile.

#### `ctxloom bundle` (Advanced)

Manage bundles directly. Most users should use `fragment` and `skill` commands instead.

| Subcommand | Description |
|------------|-------------|
| `list` | List installed bundles |
| `show <name>` | Show bundle contents |
| `view <name>` | View bundle content (supports `#path` drilling) |
| `create <name>` | Create new bundle |
| `edit <name>` | Edit bundle metadata |
| `delete <name>` | Delete bundle |
| `distill <patterns>` | Distill multiple bundles |
| `export <name> <dir>` | Export bundle to directory |
| `import <file>` | Import bundle from file |
| `push <name> [remote]` | Push bundle to remote |

**Examples:**
```bash
ctxloom bundle list
ctxloom bundle show my-bundle
ctxloom bundle view my-bundle#fragments/coding
ctxloom bundle create my-bundle --description "My standards"
ctxloom bundle distill "*.yaml" --force
ctxloom bundle export my-bundle ./exported/
```

---

### Infrastructure Commands

#### `ctxloom manage`

Install, inspect, and remove ctxloom's project harness. Everything that mutates
the harness (`.ctxloom`, hooks, statusline, MCP registration, command files,
`.gitignore`, config) lives here.

| Subcommand | Description |
|------------|-------------|
| `init` | Scaffold a new `.ctxloom` directory (top-level `ctxloom init` is an alias) |
| `install [--print]` | One-shot non-interactive setup: scaffold + gitignore + hooks/MCP/statusline |
| `uninstall` | Remove ctxloom hooks, statusline, MCP entry, and command files |
| `status` | Show what ctxloom has wired into the project |
| `hooks [install\|uninstall\|status]` | Manage ctxloom backend hooks |
| `mcp [install\|uninstall]` | Toggle auto-registration of ctxloom's own MCP server |
| `mcp servers [add\|remove\|list\|show]` | Manage configured MCP servers |
| `statusline [install\|uninstall]` | Toggle ctxloom's HUD statusline (disable to keep your own) |
| `config [show\|get\|edit\|init]` | Show or modify configuration |
| `gitignore install` | Add ctxloom's private-state and transient-artifact ignores |

**Examples:**
```bash
ctxloom manage install                      # Wire ctxloom into this project
ctxloom manage status                       # What's wired in?
ctxloom manage hooks install                # Re-apply hooks after editing bundles
ctxloom manage mcp servers add my-server -c npx -a my-mcp
ctxloom manage mcp uninstall                # Stop auto-registering ctxloom's server
ctxloom manage statusline uninstall         # Keep your own statusline, not ctxloom's HUD
ctxloom manage config show
ctxloom manage config get llm
ctxloom manage uninstall                    # Remove integration (keeps .ctxloom)
```

#### `ctxloom mcp`

Run ctxloom as an MCP server over stdio (the runtime entrypoint referenced by
generated `.mcp.json`). Server-config management lives under `ctxloom manage mcp`.

| Subcommand | Description |
|------------|-------------|
| `serve` | Run as MCP server over stdio (same as bare `ctxloom mcp`) |

**Examples:**
```bash
ctxloom mcp serve                           # Run MCP server
```

---

### Utility Commands

#### `ctxloom memory`

Manage session memory for compaction.

| Subcommand | Description |
|------------|-------------|
| `list` | List all sessions |
| `show <session>` | Show session details |
| `compact` | Compact session log |

**Examples:**
```bash
ctxloom memory list
ctxloom memory show abc123
ctxloom memory compact --session abc123
```

#### `ctxloom llm`

Manage LLM backends.

| Subcommand | Description |
|------------|-------------|
| `list` | List available LLMs |
| `default [name]` | Get/set the default LLM |

**Examples:**
```bash
ctxloom llm list
ctxloom llm default claude-code
```

#### `ctxloom version`

Print version number.

```bash
ctxloom version
```

#### `ctxloom completion`

Generate shell completion scripts.

```bash
ctxloom completion bash
ctxloom completion zsh
ctxloom completion fish
ctxloom completion powershell
```

---


## Reference Syntax

### Bundle References
```
bundle#fragments/name     # Fragment in bundle
bundle#skills/name        # Skill in bundle
bundle#mcp/name           # MCP config in bundle
```

### Remote References
```
remote/bundle             # Bundle from remote
remote/bundle@v1.0.0      # Versioned bundle
user/repo                 # GitHub shorthand
```

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `CTXLOOM_HOME` | Override default config directory |
| `EDITOR` | Editor for edit commands |
| `GITHUB_TOKEN` | Default token for the `github` forge (override per forge via `token_env`) |

---

## Configuration Files

| File | Description |
|------|-------------|
| `.ctxloom/config.yaml` | Main configuration |
| `.ctxloom/remotes.yaml` | Remote sources |
| `.ctxloom/bundles/*.yaml` | Bundle files |
| `.ctxloom/profiles/*.yaml` | Profile files |
| `.ctxloom/lock.yaml` | Dependency lockfile |
