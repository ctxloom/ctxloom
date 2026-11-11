---
tags:
  - context
  - claude-code
  - workflow
content_hash: sha256:47c4d74f5fc089f33bade4a78bfd7ae3eebdefddde34dd10c4139e9ade31d513
---
# CLAUDE.md Rules

**Size: 100-200 lines max.** Move overflow to per-folder CLAUDE.md files.

## Must Include
```markdown
# Tech Stack
Language, framework, database, testing tools with versions

# Architecture
Directory structure with purpose of each folder

# Commands
Build, test, lint, deploy commands

# Coding Rules
Project-specific conventions AI would violate otherwise
```

## Do NOT Include
- General language syntax
- Patterns already enforced by linters
- Things Claude does correctly without instruction

**Test:** Would Claude make a mistake without this line? If no, delete it.

**Maintenance:** When Claude makes a mistake, add the correction to CLAUDE.md immediately.