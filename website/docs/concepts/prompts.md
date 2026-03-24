---
sidebar_position: 3
---

# Prompts

A **prompt** is a saved prompt template within a bundle. Prompts help standardize common AI interactions.

## Prompt Structure

Prompts are defined within bundles:

```yaml
prompts:
  code-review:
    description: "Review Python code for best practices"
    content: |
      Review this Python code for adherence to best practices.

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

## Using Prompts

### Run a Saved Prompt

```bash
# Run a prompt by name
scm run -r code-review

# Include with prompt reference
scm run -f my-bundle#prompts/code-review
```

### List Available Prompts

```bash
# List prompts in a bundle
scm bundle show my-bundle
```

## Editing Prompts

```bash
# Edit prompt content in your editor
scm bundle prompt edit my-bundle code-review
```

## Prompts vs Fragments

| Aspect | Fragments | Prompts |
|--------|-----------|---------|
| Purpose | Context/instructions | Specific requests |
| Usage | Combined with user input | Standalone or combined |
| Typical content | Guidelines, patterns | Questions, review requests |

Fragments provide context; prompts provide actions. Often used together:

```bash
# Use fragment for context, prompt for the action
scm run -f python-standards -r code-review
```
