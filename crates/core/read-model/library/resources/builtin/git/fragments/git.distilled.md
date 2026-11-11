---
distilled_by: claude-code
---
# Git

Branching: no branches or PRs unless explicitly asked; short descriptive names; one branch per task; branch off main/master only — push back and confirm anything else.

Commits: terse, describe code changes only, no meta-commentary; NEVER mention Claude, Anthropic, AI, or "Generated with".

Breaking changes: <1.0 and new major versions need no backwards compat — remove deprecated code immediately; post-1.0 minor/patch, discuss before implementing.

Pre-commit: lint, format, test before committing — never commit broken code; fix pre-commit errors automatically without asking. Bypass hooks (`--no-verify`) only for WIP on feature branches, with documented reasoning.