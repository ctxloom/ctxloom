# ctxloom Development Guidelines

## Fault Handling Philosophy

ctxloom **fails loudly by default.** A broken config, an unresolvable profile
or bundle, a failed sync, or a failed hook apply is a fatal startup finding:
ctxloom prints the finding(s) with a fix-it hint and aborts before launching
(exit code 3), so a broken setup surfaces immediately instead of silently
degrading into a wrong-context session.

The escape hatch is **degraded mode** — `--degraded` (or `CTXLOOM_DEGRADED=1`)
— which downgrades those fatal findings to warnings and launches anyway
("things may be broken, get me an agent"). Degraded mode is opt-in and is the
only place the old warn-and-continue behavior lives.

### Core Principles

1. **Fail loud, fail early** - Configuration errors, unresolvable
   profiles/bundles, sync failures, and hook-apply failures are fatal findings
   that abort startup unless degraded mode is set.

2. **Route faults through `strictness`** - Do not hand-roll warn-and-continue
   on a startup fault. Call `strictness.Fail`/`FailOnce`/`Record` with a
   failure class (`internal/shared/strictness`) and a fix-it command. The
   warning always streams to stderr; in strict mode the finding is also
   collected, and the startup choke owner (`ctxloom run`/`mcp`/`acp`) aborts
   on any collected finding.

3. **The mode is flag/env only, never config** - `--degraded` /
   `CTXLOOM_DEGRADED=1`. There is deliberately NO config key: a broken config
   cannot excuse itself (bootstrap circularity).

4. **Degraded mode is where graceful degradation lives** - When the user opts
   in, `strictness.record` is a no-op, startup continues on sensible defaults,
   and "partial success is success" applies — 9 of 10 bundles synced launches
   with the 9.

### Error Handling Patterns

```go
// Good: a classified, fatal-by-default finding with a fix-it hint. The startup
// choke owner aborts on it unless --degraded is set; degraded downgrades it.
if err != nil {
    strictness.Fail(strictness.ClassSync,
        "check the remote/network, or pass --degraded to launch anyway",
        "sync failed: %v", err)
}

// Bad: raw warn-and-continue — silently ships a possibly-wrong session.
if err != nil {
    fmt.Fprintf(os.Stderr, "ctxloom: warning: sync failed: %v\n", err)
    // continue
}
```

### Startup Sequence

Each step records fatal findings through `strictness`; the choke owner checks
the collected findings once and aborts before launch (exit code 3) unless
degraded:
1. Load config (fatal on broken/lossy config)
2. Sync dependencies (fatal on sync failure)
3. Transform context files (fatal on regen failure)
4. Apply hooks (fatal on apply failure)
5. Gate: abort with the findings list — or, in degraded mode, launch anyway

## Generated Docs

Generated CLI reference pages live in `website/src/content/docs/reference/cli/ctxloom_*.md` — never hand-edit them; edit the command definitions in `internal/cli` and run `just gen-docs`.

