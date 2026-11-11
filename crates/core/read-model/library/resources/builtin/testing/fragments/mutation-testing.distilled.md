---
distilled_by: claude-code
---
# Mutation Testing

High coverage + low mutation kill rate = false confidence.

```bash
git worktree add --detach ../.mutants-worktree HEAD
cargo mutants -d ../.mutants-worktree --in-place --timeout 120 -f <file> -- --lib
git worktree remove ../.mutants-worktree --force
```

Worktree shares .git and copies only source; `--in-place` is safe because the worktree is disposable.

Kill-rate targets: pure utilities/validators 90%+; business logic/state machines 85%+; orchestration/coordinators 70%+; framework glue/adapters 50%+. Skip generated code (`*.pb.rs`, `src/proto/`), trivial delegation, framework boilerplate.