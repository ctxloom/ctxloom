---
title: "CLI Reference"
---

Complete reference for all ctxloom commands.

## ctxloom init

Initialize a new .ctxloom directory.

```bash
ctxloom init              # Create .ctxloom in current directory
ctxloom init --home       # Create/ensure ~/.ctxloom exists
```

## ctxloom run

Assemble context and run the configured LLM.

```bash
ctxloom run [flags] [prompt...]
```

### Flags

| Flag | Description |
|------|-------------|
| `-p, --profile <name>` | Load predefined fragment set |
| `-f, --fragments <names>` | Additional fragments to include (repeatable) |
| `-t, --tags <tags>` | Include fragments with specific tags (repeatable) |
| `--saved-prompt <name>` | Use saved prompt instead of inline |
| `--dry-run` | Show what would be assembled without running |
| `--suppress-warnings` | Suppress warnings |
| `-l, --llm <label>` | Config label/backend to use; overrides the profile's `llm:` and the default |
| `--print` | Run non-interactively (oneshot) and print the response |
| `-v, -vv, -vvv` | Verbosity levels |

With `--print` and no prompt argument, the prompt is read from **stdin** when
piped — making `run --print` a universal reducer over any input (e.g. the output
of `ctxloom map`, or text from another tool).

### Examples

```bash
ctxloom run -p developer "implement error handling"
ctxloom run -f python-tools#fragments/typing "add type hints"
ctxloom run -t security "check for vulnerabilities"
ctxloom run -l gemini-code "use Gemini"
ctxloom run --dry-run  # Preview only

# Synthesize piped input with a high-power profile:
cat findings.txt | ctxloom run -p code-review/synthesis --print
```

## ctxloom map

Run multiple profiles in parallel over one shared task — the **fan-out** half of
the `weave` ensemble primitive. Each `-p` profile runs as its own oneshot agent
with its own context and LLM (`llm:`), and their outputs are emitted as labeled
blocks. Runs are bounded by `--concurrency` and fault-tolerant: a failed member
is reported inline without aborting the others.

```bash
ctxloom map [flags] [task...]
```

### Flags

| Flag | Description |
|------|-------------|
| `-p, --profile <name>` | Profile to run as a parallel member (repeatable) |
| `-l, --llm <label>` | Override the LLM for every member (else each profile's own `llm:`) |
| `--concurrency <n>` | Max members to run at once (default 4) |
| `--save-parts <dir>` | Write each member's raw output into a directory |

The task is taken from the arguments, or from stdin when no arguments are given.
Pipe the labeled output into a synthesizer to complete the map → reduce flow:

### Examples

```bash
git diff | ctxloom map -p code-review/security -p code-review/performance
ctxloom map -p a -p b -p c --concurrency 3 "review this change" \
  | ctxloom run -p code-review/synthesis --print
ctxloom map -p reviewer/a -p reviewer/b --save-parts ./parts "audit"
```

## ctxloom weave

Fan a task across profiles in parallel, then **synthesize** the results — the
composite of `map` + a synthesis `run`, run in-process (no shell) so it behaves
identically on every platform. Each `-p` member runs as its own agent on its own
`llm:`; the `-s` synthesizer runs on its own (typically high-power) `llm:`.

```bash
ctxloom weave [flags] [task...]
```

### Flags

| Flag | Description |
|------|-------------|
| `-p, --profile <name>` | Member profile to run in parallel (repeatable) |
| `-s, --synthesize <name>` | Synthesis profile that combines member outputs |
| `-l, --llm <label>` | Override the LLM for every member (synthesizer keeps its own `llm:`) |
| `--concurrency <n>` | Max members to run at once (default 4) |
| `--part <NAME=FILE>` | Inject a non-ctxloom output as a labeled part (repeatable) |
| `--parts-from <dir>` | Inject every file in a directory as a part |
| `--save-parts <dir>` | Write each member's raw output into a directory |
| `--no-synthesize` | Emit the labeled parts only; skip synthesis |

`weave` is equivalent to `ctxloom map -p A -p B "task" | ctxloom run -p SYNTH
--print`, but portable and single-invocation. Use the components directly when
you want to inspect or post-process the intermediate parts; use `--part` /
`--parts-from` to synthesize **non-ctxloom outputs** alongside (or instead of)
live members.

### Examples

```bash
ctxloom weave -p code-review/security -p code-review/performance \
  -s code-review/synthesis "review this diff"
git diff | ctxloom weave -p reviewer/a -p reviewer/b -s synthesis
ctxloom weave -p a -p b -s synth --part legacy=old-report.txt "audit"
ctxloom weave -s synth --parts-from ./collected "merge these findings"
```

## ctxloom fragment

Manage fragments.

| Command | Flags | Arguments | Description |
|---------|-------|-----------|-------------|
| `list` | `--bundle` | | List all fragments, optionally filtered by bundle |
| `show` | `--distilled` | `bundle#fragments/name` | Show fragment content |
| `create` | | `<bundle> <name>` | Create new fragment with placeholder |
| `delete` | | `bundle#fragments/name` | Delete fragment from bundle |
| `edit` | | `bundle#fragments/name` | Edit fragment in configured editor |
| `distill` | `--force` | `bundle#fragments/name` | Create token-efficient version |
| `push` | | `bundle#fragments/name` | Push a fragment to its remote |

### Examples

```bash
ctxloom fragment list
ctxloom fragment list --bundle python-tools
ctxloom fragment show python-tools#fragments/typing
ctxloom fragment show --distilled python-tools#fragments/typing
ctxloom fragment create my-bundle coding-standards
ctxloom fragment edit my-bundle#fragments/coding-standards
ctxloom fragment distill my-bundle#fragments/coding-standards

# Consume a remote bundle (or one fragment) by referencing it from a local
# profile, then pulling. ctxloom resolves the reference and updates the lockfile.
ctxloom profile create testing -b ctxloom-default/testing
ctxloom profile create security -b ctxloom-default/security#fragments/owasp-top-10
ctxloom remote pull
```

## ctxloom prompt

Manage prompts.

| Command | Arguments | Description |
|---------|-----------|-------------|
| `list` | | List all prompts |
| `show` | `bundle#prompts/name` | Show prompt content |
| `create` | `<bundle> <name>` | Create new prompt |
| `delete` | `bundle#prompts/name` | Delete prompt |
| `edit` | `bundle#prompts/name` | Edit prompt in editor |
| `push` | `bundle#prompts/name` | Push a prompt to its remote |

## ctxloom profile

Manage profiles.

| Command | Flags | Arguments | Description |
|---------|-------|-----------|-------------|
| `list` | | | List all profiles |
| `show` | | `<name>` | Show profile details and exclusions |
| `create` | `--parent`, `-b`, `-d` | `<name>` | Create new profile |
| `modify` | See below | `<name>` | Modify profile configuration |
| `default` | `--unset` | `[name\|ref]` | Set, clear, or show the default profile(s) |
| `delete` | | `<name>` | Delete profile |
| `edit` | | `<name>` | Edit profile in editor |
| `export` | | `<name>` | Export a profile |
| `import` | | `<file>` | Import a profile |
| `push` | | `<name>` | Push a profile to its remote |

### Create Flags

| Flag | Description |
|------|-------------|
| `--parent` | Parent profiles to inherit from (repeatable) |
| `-b, --bundle` | Bundle references to include (repeatable) |
| `-d, --description` | Profile description |
| `--llm` | Preferred LLM config label/backend to launch (overridable by `run -l`) |

### Modify Flags

| Flag | Description |
|------|-------------|
| `--add-parent` | Add parent profile (repeatable) |
| `--llm` | Set the preferred LLM (a config label/backend); empty clears it |
| `--remove-parent` | Remove parent profile (repeatable) |
| `--add-bundle` | Add bundle reference (repeatable) |
| `--remove-bundle` | Remove bundle reference (repeatable) |
| `-d, --description` | Update description |
| `--exclude-fragment` | Add fragment to exclusion list (repeatable) |
| `--include-fragment` | Remove fragment from exclusion list (repeatable) |
| `--exclude-prompt` | Add prompt to exclusion list (repeatable) |
| `--include-prompt` | Remove prompt from exclusion list (repeatable) |
| `--exclude-mcp` | Add MCP server to exclusion list (repeatable) |
| `--include-mcp` | Remove MCP server from exclusion list (repeatable) |

### Examples

```bash
ctxloom profile list
ctxloom profile show developer
ctxloom profile create my-profile -b python-tools -d "My dev profile"
ctxloom profile create child --parent base --parent security -b extras
ctxloom profile modify developer --exclude-fragment verbose-logging
ctxloom profile modify developer --include-mcp slow-server
ctxloom profile edit my-profile

# Inherit a remote profile: reference it as a parent, then pull.
ctxloom profile create python-dev --parent ctxloom-default/python-developer
ctxloom remote pull

# Set, clear, or show the default profile(s) (defaults are a list; an entry may
# be a local profile name or a remote ref).
ctxloom profile default python-dev
ctxloom profile default ctxloom-default/python-developer
ctxloom profile default --unset python-dev
ctxloom profile default                       # show current default(s)
```

### Bare convenience refs

`profile create` and `profile modify` accept **bare convenience refs** for
`-b`/`--bundle` and `--parent` (e.g. `-b code-review-base#fragments/conduct` or
`--parent developer`). Bare refs are expanded against the configured default
remote into canonical URLs. Full URLs
(`https://github.com/owner/repo@bundles/name`) and `ctxloom:local@...` refs pass
through unchanged.

## ctxloom remote

Manage remote sources.

| Command | Arguments | Description |
|---------|-----------|-------------|
| `add` | `<name> <url>` | Register remote source |
| `remove` | `<name>` | Remove registered remote |
| `list` | | List configured remotes |
| `default` | `[name]` | Get/set default remote |
| `search` | `<query>` | Search for bundles/profiles |
| `browse` | `<remote>` | Browse remote contents |
| `trust` | `<name>` | Mark a remote as trusted (auto-applies upgrades) |
| `untrust` | `<name>` | Revoke trust for a remote |
| `discover` | | Find ctxloom repos on GitHub/GitLab |
| `pull` | | Fetch all remote bundles/profiles referenced by your local profiles, update the lockfile, and apply hooks |
| `update` | `[name]` | Report the newest commit available within each constraint |
| `upgrade` | | Re-resolve within constraints and move the **lock** (trusted apply, others stage for review) |

A reference's `@version` is a **constraint** — a semver range (`@^1.2`), a branch
(`@main`), an exact tag/SHA (`@v1.2.3`), or empty (default branch). `upgrade`
moves only the lockfile, never your profile YAML. Freeze an item with `ctxloom
bundle hold <name>` (alias `pin`); release with `unhold` (alias `unpin`). See
[Versioning, locking, and holds](/concepts/remotes/#versioning-locking-and-holds).

### URL Formats

| Format | Example |
|--------|---------|
| GitHub shorthand | `alice/ctxloom` |
| Full HTTPS | `https://github.com/alice/ctxloom` |
| GitLab | `https://gitlab.com/corp/ctxloom` |
| SSH (converted to HTTPS) | `git@github.com:alice/ctxloom.git` |

### Examples

```bash
ctxloom remote add myteam myorg/ctxloom-team
ctxloom remote add corp https://gitlab.com/corp/ctxloom
ctxloom remote list
ctxloom remote default myteam
ctxloom remote browse ctxloom-default
ctxloom remote search "python testing"
ctxloom remote discover
ctxloom remote pull
```

## ctxloom mcp

Manage MCP server configuration.

| Command | Flags | Arguments | Description |
|---------|-------|-----------|-------------|
| `serve` | | | Run as MCP server over stdio (default) |
| `list` | | | List configured MCP servers |
| `add` | `-c`, `-a`, `-b` | `<name>` | Add MCP server |
| `remove` | `-b` | `<name>` | Remove MCP server |
| `show` | | `<name>` | Show MCP server details |
| `auto-register` | `--disable`, `--enable` | | Configure auto-registration |

### Add Flags

| Flag | Description |
|------|-------------|
| `-c, --command` | Command to run (required) |
| `-a, --args` | Command arguments (repeatable) |
| `-b, --backend` | Backend scope: `unified`, `claude-code`, `gemini` |

### Examples

```bash
ctxloom mcp serve                    # Run as MCP server
ctxloom manage mcp servers list
ctxloom manage mcp servers add tree-sitter -c "npx" -a "tree-sitter-mcp" -a "--stdio"
ctxloom manage mcp servers add my-server -c "/path/to/server" -b claude-code
ctxloom manage mcp servers remove tree-sitter
ctxloom manage mcp uninstall
```

## ctxloom memory

Manage session memory.

| Command | Flags | Description |
|---------|-------|-------------|
| `compact` | `--session`, `--model` | Compact session to distilled summary |
| `list` | `--backend` | List sessions with compaction status |
| `load` | `--model` | Load and distill a specific session |
| `query` | `--limit` | Semantic search across session history |

### Examples

```bash
ctxloom memory compact                       # Compact current session
ctxloom memory compact --session abc123      # Compact specific session
ctxloom memory list                          # List all sessions
ctxloom memory list --backend gemini         # List Gemini sessions
ctxloom memory load abc123def                # Load specific session
ctxloom memory query "authentication flow"  # Search session history
```

## ctxloom completion

Generate shell completion scripts.

```bash
ctxloom completion [bash|zsh|fish|powershell]
```

### Installation

**Bash:**
```bash
source <(ctxloom completion bash)                        # Current session
ctxloom completion bash > /etc/bash_completion.d/ctxloom # Permanent (Linux)
ctxloom completion bash > /usr/local/etc/bash_completion.d/ctxloom   # macOS
```

**Zsh:**
```bash
echo "autoload -U compinit; compinit" >> ~/.zshrc  # Enable if needed
ctxloom completion zsh > "${fpath[1]}/_ctxloom"
```

**Fish:**
```fish
ctxloom completion fish > ~/.config/fish/completions/ctxloom.fish
```

**PowerShell:**
```powershell
ctxloom completion powershell | Out-String | Invoke-Expression
```
