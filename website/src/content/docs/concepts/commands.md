---
title: "Commands"
---

You've got a five-paragraph code-review request you paste into every PR, the one that reminds the AI to check error handling and watch for N+1 queries. Or you don't, because retyping it every time is tedious enough that you skip it on the small changes — the ones that turn out to matter anyway.

A **command** saves that request once in a bundle and, once trusted, exposes it as a slash command in Claude Code, Codex, Kiro, or the Antigravity CLI, so invoking it costs one line instead of five paragraphs. (Earlier ctxloom versions called this item kind "prompts", then "skills"; bundles using the old `prompts:`/`skills:` key are migrated on load.)

## Command Structure

Commands are defined within bundles:

```yaml
commands:
  code-review:
    description: "Review code for best practices"
    content: |
      Review this code for adherence to best practices.

      Focus on:
      - Error handling
      - Type annotations
      - Documentation
      - Performance considerations

  refactor:
    description: "Suggest refactoring improvements"
    content: |
      Analyze this code and suggest refactoring improvements.

      Consider:
      - SOLID principles
      - Code clarity
      - Testability
```

## Slash Command Integration

**A trusted command is exposed as a slash command.** Command export is a trust choke: a command from a bundle that's still pending review isn't written out at all — only local, builtin, trusted-signer, or already-approved content reaches your AI CLI. See [Review & Trust](/concepts/review-and-trust/).

The slash command name isn't the bare command name — it's `<bundle>-<command>`, taken from the owning bundle's last path segment. A `code-review` command defined in a bundle called `my-bundle` becomes:

```bash
# Claude Code, Codex, Kiro, or Antigravity CLI:
/my-bundle-code-review
```

Only a builtin command (one with no bundle metadata) falls back to its bare name.

ctxloom writes command files to the appropriate location:
- **Claude Code**: `.claude/commands/*.md` (nested names flatten: `/` becomes `-` in the filename)
- **Codex**: `$CODEX_HOME/prompts/*.md` — global, not project-scoped (Codex only discovers prompts there)
- **Kiro**: `.kiro/skills/<bundle>-<command>/SKILL.md` — one directory per command
- **Antigravity CLI**: `.agents/skills/<bundle>/*.md` (subdirectories are preserved, not flattened)

### Command Configuration

Control how commands appear as slash commands per backend:

```yaml
commands:
  code-review:
    description: "Review code for best practices"
    content: |
      Review code...
    llm:
      claude-code:
        enabled: true              # Default: true (opt-out model)
        description: "Review code" # Shown in /help
        argument_hint: "<file>"    # Autocomplete hint
        allowed_tools:             # Restrict available tools
          - Read
          - Grep
        model: "sonnet"            # Override model
      antigravity:
        enabled: true              # Also expose in Antigravity CLI
        description: "Review code"
      codex:
        enabled: true              # Also expose as a Codex custom prompt
        description: "Review code"
        argument_hint: "<file>"
      kiro:
        enabled: true              # Also expose as a Kiro skill
        description: "Review code"
```

The `llm:` map has one key per backend: `claude-code`, `antigravity`, `codex`, `kiro`.

### Configuration Fields

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `true` | Set to `false` to hide from slash commands |
| `description` | command description | Short description for `/help` |
| `argument_hint` | none | Hint shown during autocomplete (Claude, Codex) |
| `allowed_tools` | all | Restrict which tools the command can use (Claude only) |
| `model` | default | Override the model (Claude only) |

### Disabling a Command

To keep a command but not expose it as a slash command:

```yaml
commands:
  internal-command:
    description: "Internal use only"
    content: |
      This command is used programmatically, not as a slash command.
    llm:
      claude-code:
        enabled: false
      antigravity:
        enabled: false
```

## Using Commands

### As Slash Commands

```bash
# In Claude Code, Codex, Kiro, or Antigravity CLI, just use the slash command
# (a `code-review` command in bundle `my-bundle` exports as /my-bundle-code-review):
/my-bundle-code-review

# With arguments:
/my-bundle-code-review src/main.go
```

### Via CLI

```bash
# Run a saved command by name
ctxloom run -r code-review
```

### List Available Commands

```bash
# List all commands
ctxloom command list

# Show command details
ctxloom command show my-bundle#commands/code-review
```

## Editing Commands

```bash
# Edit command content in your editor
ctxloom command edit my-bundle#commands/code-review
```

## Commands vs Fragments

| Aspect | Fragments | Commands |
|--------|-----------|--------|
| Purpose | Context/instructions | Specific actions/requests |
| Usage | Combined with user input | Standalone commands or combined |
| Typical content | Guidelines, patterns, standards | Review requests, generation tasks |
| In Claude/Codex/Kiro/Antigravity | Injected as context | Exposed as slash commands (once trusted) |

**Fragments** provide context that's always available. **Commands** provide specific actions you invoke when needed.

### Using Together

```bash
# Fragment provides context, command defines the action
ctxloom run -f python-standards -r code-review

# In Claude Code, Codex, Kiro, or Antigravity CLI:
# 1. Context from fragments is already injected
# 2. Just invoke the command:
/my-bundle-code-review
```

## Examples

### Code Review Command

```yaml
commands:
  review:
    description: "Comprehensive code review"
    tags: [review, quality]
    content: |
      Perform a code review:

      1. **Correctness**: Logic errors, edge cases
      2. **Security**: OWASP top 10, input validation
      3. **Performance**: N+1 queries, unnecessary allocations
      4. **Maintainability**: Naming, complexity, documentation
      5. **Testing**: Coverage gaps, test quality

      Provide specific line references and suggested fixes.
    llm:
      claude-code:
        description: "Comprehensive code review"
        argument_hint: "<file or directory>"
```

### Test Generator Command

```yaml
commands:
  gen-tests:
    description: "Generate unit tests"
    content: |
      Generate unit tests for the specified code.

      Requirements:
      - Use table-driven tests where appropriate
      - Cover happy path and error cases
      - Mock external dependencies
      - Include edge cases
    llm:
      claude-code:
        description: "Generate unit tests"
        argument_hint: "<function or file>"
        allowed_tools:
          - Read
          - Write
          - Grep
```

### Documentation Command

```yaml
commands:
  document:
    description: "Generate documentation"
    content: |
      Generate documentation for the specified code:

      - Function/method signatures with descriptions
      - Parameter explanations
      - Return value descriptions
      - Usage examples
      - Error conditions
    llm:
      claude-code:
        description: "Generate docs"
        model: "haiku"  # Use faster model for docs
```
