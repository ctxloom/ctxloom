---
title: "Commands"
---

You've got a five-paragraph code-review request you paste into every PR, the one that reminds the AI to check error handling and watch for N+1 queries. Or you don't, because retyping it every time is tedious enough that you skip it on the small changes — the ones that turn out to matter anyway.

A **command** saves that request once in a bundle and, once trusted, exposes it as a slash command in your engine (Claude Code), so invoking it costs one line instead of five paragraphs. (A bundle that still uses the older `prompts:` key is read as `commands:`. `skills:` is a separate item kind: Agent Skill directories.)

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

**A trusted command is exposed as a slash command.** Command export is a trust choke: a command from a bundle that's still pending review isn't written out at all — only local, companion, trusted-signer, or already-approved content reaches your AI CLI. See [Review & Trust](/concepts/review-and-trust/).

The slash command name isn't the bare command name — it's `<bundle>-<command>`, taken from the owning bundle's last path segment. A `code-review` command defined in a bundle called `my-bundle` becomes:

```bash
# Claude Code:
/my-bundle-code-review
```

A command with no owning bundle keeps its own name. When two commands would export under the same short name, each is exported under its full identity instead, so neither overwrites the other's file.

ctxloom writes command files to the appropriate location:
- **Claude Code**: the session's own `commands/` directory, or `.claude/commands/` when the agent binding selects the project root (nested names flatten: `/` becomes `-` in the filename)

### Command Configuration

Control how commands appear as slash commands per engine:

```yaml
commands:
  code-review:
    description: "Review code for best practices"
    content: |
      Review code...
    exports:
      claude-code:
        enabled: true              # Default: true (opt-out model)
        description: "Review code" # Shown in /help
        argument_hint: "<file>"    # Autocomplete hint
        allowed_tools:             # Restrict available tools
          - Read
          - Grep
        model: "sonnet"            # Override model
```

The `exports:` map has one key per engine name.

### Configuration Fields

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `true` | Set to `false` to hide from slash commands |
| `description` | command description | Short description for `/help` |
| `argument_hint` | none | Hint shown during autocomplete |
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
    exports:
      claude-code:
        enabled: false
```

## Using Commands

### As Slash Commands

```bash
# In Claude Code, just use the slash command
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
| In the engine | Injected as context | Exposed as slash commands (once trusted) |

**Fragments** provide context that's always available. **Commands** provide specific actions you invoke when needed.

### Using Together

```bash
# Fragment provides context, command defines the action
ctxloom run -f python-standards -r code-review

# In Claude Code:
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
    exports:
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
    exports:
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
    exports:
      claude-code:
        description: "Generate docs"
        model: "haiku"  # Use faster model for docs
```
