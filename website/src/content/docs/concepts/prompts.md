---
title: "Prompts"
---

A **prompt** is a saved prompt template within a bundle. Prompts help standardize common AI interactions and are automatically exposed as **slash commands** in both Claude Code and Gemini CLI.

## Prompt Structure

Prompts are defined within bundles:

```yaml
prompts:
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

**Prompts are automatically exposed as slash commands.** When you define a prompt, it becomes available in your AI CLI:

```bash
# Claude Code or Gemini CLI:
/code-review
/refactor
```

ctxloom writes command files to the appropriate location:
- **Claude Code**: `.claude/commands/ctxloom/*.md`
- **Gemini CLI**: `.gemini/commands/ctxloom/*.toml`

### Command Configuration

Control how prompts appear as slash commands per backend:

```yaml
prompts:
  code-review:
    description: "Review code for best practices"
    content: |
      Review this code...
    plugins:
      llm:
        claude-code:
          enabled: true              # Default: true (opt-out model)
          description: "Review code" # Shown in /help
          argument_hint: "<file>"    # Autocomplete hint
          allowed_tools:             # Restrict available tools
            - Read
            - Grep
          model: "sonnet"            # Override model
        gemini:
          enabled: true              # Also expose in Gemini CLI
          description: "Review code"
```

### Configuration Fields

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `true` | Set to `false` to hide from slash commands |
| `description` | prompt description | Short description for `/help` |
| `argument_hint` | none | Hint shown during autocomplete (Claude only) |
| `allowed_tools` | all | Restrict which tools the command can use (Claude only) |
| `model` | default | Override the model (Claude only) |

### Disabling a Command

To keep a prompt but not expose it as a slash command:

```yaml
prompts:
  internal-prompt:
    description: "Internal use only"
    content: |
      This prompt is used programmatically, not as a command.
    plugins:
      llm:
        claude-code:
          enabled: false
        gemini:
          enabled: false
```

## Using Prompts

### As Slash Commands

```bash
# In Claude Code or Gemini CLI, just use the slash command:
/code-review
/refactor

# With arguments:
/code-review src/main.go
```

### Via CLI

```bash
# Run a prompt by name
ctxloom run --saved-prompt code-review

# Include with prompt reference
ctxloom run -f my-bundle#prompts/code-review
```

### List Available Prompts

```bash
# List all prompts
ctxloom prompt list

# Show prompt details
ctxloom prompt show my-bundle#prompts/code-review
```

## Editing Prompts

```bash
# Edit prompt content in your editor
ctxloom prompt edit my-bundle#prompts/code-review
```

## Prompts vs Fragments

| Aspect | Fragments | Prompts |
|--------|-----------|---------|
| Purpose | Context/instructions | Specific actions/requests |
| Usage | Combined with user input | Standalone commands or combined |
| Typical content | Guidelines, patterns, standards | Review requests, generation tasks |
| In Claude/Gemini | Injected as context | Exposed as slash commands |

**Fragments** provide context that's always available. **Prompts** provide specific actions you invoke when needed.

### Using Together

```bash
# Fragment provides context, prompt defines the action
ctxloom run -f python-standards --saved-prompt code-review

# In Claude Code or Gemini CLI:
# 1. Context from fragments is already injected
# 2. Just invoke the command:
/code-review
```

## Examples

### Code Review Skill

```yaml
prompts:
  review:
    description: "Comprehensive code review"
    tags: [review, quality]
    content: |
      Perform a comprehensive code review:

      1. **Correctness**: Logic errors, edge cases
      2. **Security**: OWASP top 10, input validation
      3. **Performance**: N+1 queries, unnecessary allocations
      4. **Maintainability**: Naming, complexity, documentation
      5. **Testing**: Coverage gaps, test quality

      Provide specific line references and suggested fixes.
    plugins:
      llm:
        claude-code:
          description: "Comprehensive code review"
          argument_hint: "<file or directory>"
```

### Test Generator Skill

```yaml
prompts:
  gen-tests:
    description: "Generate unit tests"
    content: |
      Generate comprehensive unit tests for the specified code.

      Requirements:
      - Use table-driven tests where appropriate
      - Cover happy path and error cases
      - Mock external dependencies
      - Include edge cases
    plugins:
      llm:
        claude-code:
          description: "Generate unit tests"
          argument_hint: "<function or file>"
          allowed_tools:
            - Read
            - Write
            - Grep
```

### Documentation Skill

```yaml
prompts:
  document:
    description: "Generate documentation"
    content: |
      Generate documentation for the specified code:

      - Function/method signatures with descriptions
      - Parameter explanations
      - Return value descriptions
      - Usage examples
      - Error conditions
    plugins:
      llm:
        claude-code:
          description: "Generate docs"
          model: "haiku"  # Use faster model for docs
```
