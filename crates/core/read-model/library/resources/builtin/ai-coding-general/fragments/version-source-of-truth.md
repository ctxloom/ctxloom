---
tags:
  - ai
  - coding
  - best-practices
  - workflow
  - versioning
  - configuration
  - dependencies
  - lockfiles
content_hash: sha256:b1b62092989317ac96114335f7a601ac6c1c923a73fe56421782bb5ff62ba643
---
# Versions: Source of Truth

Do not bake version specifications into AI context files. Project configuration and lockfiles are the source of truth.

## Why Not a Static Table

A version table baked into AI context ages out the moment the project moves. The lockfile is always current. The lockfile is the contract; the table is a tombstone.

## What the AI Should Do

On session start, read the project's lockfile and language-pin files. Use the versions declared there. If a feature requires a newer version, propose the version bump through the lockfile, not as a one-off in code.

## Verification

```
Your suggestion uses [pattern]. Project lockfile pins [package] at [version]. Confirm the pattern is supported at that version, or propose updating the lockfile.
```