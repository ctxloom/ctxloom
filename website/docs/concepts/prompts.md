---
sidebar_position: 3
---

# Prompts

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

SCM writes command files to the appropriate location:
- **Claude Code**: `.claude/commands/scm/*.md`
- **Gemini CLI**: `.gemini/commands/scm/*.toml`

### Skill Configuration

Control how prompts appear as skills:

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
          model: "sonnet"            # Override model for this skill
```

### Skill Fields

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `true` | Set to `false` to hide from skills |
| `description` | prompt description | Short description for `/help` |
| `argument_hint` | none | Hint shown during autocomplete |
| `allowed_tools` | all | Restrict which tools the skill can use |
| `model` | default | Override the model (sonnet, opus, haiku) |

### Disabling a Skill

To keep a prompt but not expose it as a skill:

```yaml
prompts:
  internal-prompt:
    description: "Internal use only"
    content: |
      This prompt is used programmatically, not as a skill.
    plugins:
      llm:
        claude-code:
          enabled: false
```

## Using Prompts

### As Claude Code Skills

```bash
# In Claude Code session, just use the slash command:
/code-review
/refactor

# With arguments:
/code-review src/main.go
```

### Via CLI

```bash
# Run a prompt by name
scm run --saved-prompt code-review

# Include with prompt reference
scm run -f my-bundle#prompts/code-review
```

### List Available Prompts

```bash
# List all prompts
scm prompt list

# Show prompt details
scm prompt show my-bundle#prompts/code-review
```

## Editing Prompts

```bash
# Edit prompt content in your editor
scm prompt edit my-bundle#prompts/code-review
```

## Prompts vs Fragments

| Aspect | Fragments | Prompts |
|--------|-----------|---------|
| Purpose | Context/instructions | Specific actions/requests |
| Usage | Combined with user input | Standalone skills or combined |
| Typical content | Guidelines, patterns, standards | Review requests, generation tasks |
| Claude Code | Injected as context | Exposed as slash commands |

**Fragments** provide context that's always available. **Prompts** provide specific actions you invoke when needed.

### Using Together

```bash
# Fragment provides context, prompt defines the action
scm run -f python-standards --saved-prompt code-review

# In Claude Code with skills:
# 1. Context from fragments is already injected
# 2. Just invoke the skill:
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
