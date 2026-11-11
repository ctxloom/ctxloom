---
distilled_by: claude-code
---
Never bake versions into AI context files; lockfiles are the source of truth (a static table ages out the moment the project moves). On session start read the lockfile and language-pin files; propose upgrades through the lockfile, never as one-offs in code. Verification prompt: "Your suggestion uses [pattern]. Project lockfile pins [package] at [version]. Confirm the pattern is supported at that version, or propose updating the lockfile."