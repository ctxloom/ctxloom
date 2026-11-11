---
tags:
  - testing
  - mutation
  - quality
content_hash: sha256:1e78ecee84c8184dba995339adf41a4ba34d524ff5068e497605574550d1d8a5
---
# Mutation Testing

Validate test quality by verifying tests catch code mutations. High coverage + low mutation kill rate = false confidence.

## Workflow

```bash
git worktree add --detach ../.mutants-worktree HEAD
cargo mutants -d ../.mutants-worktree --in-place --timeout 120 -f <file> -- --lib
git worktree remove ../.mutants-worktree --force
```

**Why worktree?** Shares .git (~300MB), copies only source (~10MB). `--in-place` safe since worktree is disposable.

## Target Kill Rates

| Code Type | Target |
|-----------|--------|
| Pure utilities, validators | 90%+ |
| Business logic, state machines | 85%+ |
| Orchestration, coordinators | 70%+ |
| Framework glue, adapters | 50%+ |

## Skip

- Generated code (`*.pb.rs`, `src/proto/`)
- Trivial delegation
- Framework boilerplate