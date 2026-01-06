# Prototype Fragment

**This fragment is for prototyping only. Do not use in production code.**

## Purpose

The prototype fragment instructs AI to aggressively break backwards compatibility, delete deprecated code, and rebuild things correctly without compromise. This is useful during early development when:

- The codebase has no external consumers
- Breaking changes are free
- Speed of iteration matters more than stability

## Not in Default Configurations

This fragment is intentionally excluded from all default profiles. You must explicitly request it:

```bash
scm run -p prototype "build this feature"
scm run -f patterns/prototype "refactor this"
```

## When to Use

- Greenfield projects with no users
- Personal projects where you control all consumers
- Throwaway prototypes and experiments
- Early-stage development before any release

## When NOT to Use

- Production code with users
- Libraries with external consumers
- Projects past v1.0 (or any released version)
- Anything where breaking changes have real cost

## What It Does

When loaded, the AI will:

- Delete deprecated code immediately instead of marking it
- Remove old APIs entirely instead of maintaining compatibility
- Break external dependencies that require compromise
- Refuse to add compatibility shims, feature flags, or fallbacks
- Treat "this will break X" as instruction to fix X, not preserve broken behavior
