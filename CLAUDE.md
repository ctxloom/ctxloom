<!-- SCM:MANAGED
  Source: llm.md | Backend: claude-code

  This file is auto-generated from llm.md by SCM.
  Edit llm.md instead - changes sync to all configured LLM backends on startup.

  To disable sync and manage this file manually, delete this header block.
  SCM will not modify files without the SCM:MANAGED marker.
-->

# SCM Development Guidelines

## Fault Tolerance Philosophy

ctxloom should be **fault tolerant** above all else. Even through most misconfigurations, the user should still end up in their defined LLM at the end of startup.

### Core Principles

1. **Never block startup** - Configuration errors, missing files, network failures, and sync issues should produce warnings but never prevent the LLM from starting.

2. **Degrade gracefully** - If a feature fails to initialize, disable that feature and continue. The user can still work, just with reduced functionality.

3. **Log, don't crash** - All errors should be logged to stderr with clear "ctxloom: warning:" prefixes so users can diagnose issues without losing their session.

4. **Sensible defaults** - When configuration is missing or invalid, fall back to reasonable defaults rather than erroring.

5. **Partial success is success** - If 9 out of 10 bundles sync successfully, report the failure but continue with what worked.

### Error Handling Patterns

```go
// Good: warn and continue
result, err := operations.SyncOnStartup(ctx, cfg)
if err != nil {
    fmt.Fprintf(os.Stderr, "ctxloom: warning: sync failed: %v\n", err)
    // Continue - don't return error
}

// Bad: fail on error
if err != nil {
    return fmt.Errorf("sync failed: %w", err)
}
```

### Startup Sequence

The MCP server startup should:
1. Load config (warn on errors, use empty config)
2. Sync dependencies (warn on errors, continue)
3. Transform context files (warn on errors, continue)
4. Apply hooks (warn on errors, continue)
5. **Always respond with initialized** - the agent must start

