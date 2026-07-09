---
title: "Skills"
---

A **skill** is a saved prompt template within a bundle. Skills standardize common AI interactions and are automatically exposed as **slash commands** in both Claude Code and the Antigravity CLI. (Earlier ctxloom versions called this item kind "prompts"; bundles using the old `prompts:` key are migrated on load.)

## Skill Structure

Skills are defined within bundles:

```yaml
skills:
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

**Skills are automatically exposed as slash commands.** When you define a skill, it becomes available in your AI CLI:

```bash
# Claude Code or Antigravity CLI:
/code-review
/refactor
```

ctxloom writes command files to the appropriate location:
- **Claude Code**: `.claude/commands/*.md`
- **Antigravity CLI**: `.agents/skills/*.md`

### Command Configuration

Control how skills appear as slash commands per backend:

```yaml
skills:
  code-review:
    description: "Review code for best practices"
    content: |
      Review this code...
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
```

### Configuration Fields

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `true` | Set to `false` to hide from slash commands |
| `description` | skill description | Short description for `/help` |
| `argument_hint` | none | Hint shown during autocomplete (Claude only) |
| `allowed_tools` | all | Restrict which tools the command can use (Claude only) |
| `model` | default | Override the model (Claude only) |

### Disabling a Command

To keep a skill but not expose it as a slash command:

```yaml
skills:
  internal-skill:
    description: "Internal use only"
    content: |
      This skill is used programmatically, not as a command.
    llm:
      claude-code:
        enabled: false
      antigravity:
        enabled: false
```

## Using Skills

### As Slash Commands

```bash
# In Claude Code or Antigravity CLI, just use the slash command:
/code-review
/refactor

# With arguments:
/code-review src/main.go
```

### Via CLI

```bash
# Run a saved skill by name
ctxloom run -r code-review
```

### List Available Skills

```bash
# List all skills
ctxloom skill list

# Show skill details
ctxloom skill show my-bundle#skills/code-review
```

## Editing Skills

```bash
# Edit skill content in your editor
ctxloom skill edit my-bundle#skills/code-review
```

## Skills vs Fragments

| Aspect | Fragments | Skills |
|--------|-----------|--------|
| Purpose | Context/instructions | Specific actions/requests |
| Usage | Combined with user input | Standalone commands or combined |
| Typical content | Guidelines, patterns, standards | Review requests, generation tasks |
| In Claude/Antigravity | Injected as context | Exposed as slash commands |

**Fragments** provide context that's always available. **Skills** provide specific actions you invoke when needed.

### Using Together

```bash
# Fragment provides context, skill defines the action
ctxloom run -f python-standards -r code-review

# In Claude Code or Antigravity CLI:
# 1. Context from fragments is already injected
# 2. Just invoke the command:
/code-review
```

## Examples

### Code Review Skill

```yaml
skills:
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

### Test Generator Skill

```yaml
skills:
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

### Documentation Skill

```yaml
skills:
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
