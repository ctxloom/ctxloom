---
tags:
  - git
  - workflow
installation: |-
  Add submodule write-protection to `.claude/settings.json` so the harness enforces the policy rather than relying on prompt guidance alone:

  ```json
  {
    "permissions": {
      "deny": [
        "Bash(git submodule*)",
        "Bash(git -c * submodule*)",
        "Edit(.gitmodules)",
        "Write(.gitmodules)"
      ]
    }
  }
  ```

  For each path returned by `git submodule status`, also add `Edit(<path>/**)` and `Write(<path>/**)` deny entries so direct file edits inside a submodule are blocked.

  To perform a one-time approved submodule write, remove the relevant deny rule, execute the change, then restore the rule. Do not leave deny rules disabled.
content_hash: sha256:d76a5d6cab1c67a37728b20768fd5791fc309959fcc43a553d683d6f3aa021da
---
# Git Practices

## Branching Workflow

- Do not create branches or PRs unless explicitly asked
- When creating a branch, use a short descriptive name
- Keep all changes for a given task within a single branch
- Always branch off `main` or `master`. If asked to branch off anything else, push back and confirm before proceeding

## Commit Messages

- Keep commit messages straightforward and to the point; describe the code changes only
- Do not include extraneous details or meta-commentary
- **NEVER mention Claude, Anthropic, AI, or "Generated with" in commit messages**

## Breaking Changes

- **Version <1.0:** No backwards compatibility required. Clean up deprecated code immediately.
- **New major versions:** No backwards compatibility required. Remove deprecated APIs.
- **Post-1.0 minor/patch:** Discuss breaking changes before implementing

## Pre-commit

- Run linters, formatters, and tests before committing; do not commit broken code
- Fix pre-commit errors automatically without asking

## Bypassing Hooks

- Bypass only for WIP on feature branches
- Document bypass with `--no-verify` and reasoning