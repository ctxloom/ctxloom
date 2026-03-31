# AI Coding CLI Hooks - Master Comparison

Comparison of hook systems across Codex CLI (OpenAI), Gemini CLI (Google), and Claude Code (Anthropic).

## Hook Event Mapping

| Canonical Event | Codex CLI | Gemini CLI | Claude Code | Notes |
|----------------|-----------|------------|-------------|-------|
| **Session Lifecycle** |||||
| Session start | `SessionStart` | `SessionStart` | `SessionStart` | All three - identical |
| Session end | — | `SessionEnd` | `SessionEnd` | Identical |
| **User Input** |||||
| Prompt submitted | `UserPromptSubmit` | `BeforeAgent` | `UserPromptSubmit` | Codex/Claude identical; Gemini alias |
| **Tool Execution** |||||
| Before tool | `PreToolUse` | `BeforeTool` | `PreToolUse` | Codex/Claude identical; Gemini alias |
| After tool success | `PostToolUse` | `AfterTool` | `PostToolUse` | Codex/Claude identical; Gemini alias |
| After tool failure | — | — | `PostToolUseFailure` | Claude only |
| Permission dialog | — | — | `PermissionRequest` | Claude only |
| **Turn Completion** |||||
| Turn complete | `Stop` | `AfterAgent` | `Stop` | Codex/Claude identical; Gemini alias |
| Turn failed (API error) | — | — | `StopFailure` | Claude only |
| **Model/LLM Layer** |||||
| Before LLM request | — | `BeforeModel` | — | Gemini only |
| After LLM response | — | `AfterModel` | — | Gemini only |
| Before tool selection | — | `BeforeToolSelection` | — | Gemini only |
| **Context Compaction** |||||
| Before compact | — | `PreCompress` | `PreCompact` | Aliases |
| After compact | — | — | `PostCompact` | Claude only |
| **Notification** |||||
| System notification | — | `Notification` | `Notification` | Identical |
| **Subagents/Tasks** |||||
| Subagent spawned | — | — | `SubagentStart` | Claude only |
| Subagent finished | — | — | `SubagentStop` | Claude only |
| Task created | — | — | `TaskCreated` | Claude only |
| Task completed | — | — | `TaskCompleted` | Claude only |
| Teammate idle | — | — | `TeammateIdle` | Claude only |
| **File/Config Watching** |||||
| File changed | — | — | `FileChanged` | Claude only |
| Directory changed | — | — | `CwdChanged` | Claude only |
| Config changed | — | — | `ConfigChange` | Claude only |
| Instructions loaded | — | — | `InstructionsLoaded` | Claude only |
| **Worktree** |||||
| Worktree create | — | — | `WorktreeCreate` | Claude only |
| Worktree remove | — | — | `WorktreeRemove` | Claude only |
| **MCP Elicitation** |||||
| MCP input request | — | — | `Elicitation` | Claude only |
| MCP input response | — | — | `ElicitationResult` | Claude only |

## Summary

| Tool | Total Hooks | Unique Hooks |
|------|-------------|--------------|
| **Codex CLI** | 5 | 0 |
| **Gemini CLI** | 11 | 3 (`BeforeModel`, `AfterModel`, `BeforeToolSelection`) |
| **Claude Code** | 23 | 15 |

## Alias Groups (Same Behavior, Different Names)

These hooks have the same semantics but different naming conventions:

1. **Pre-tool hook**: `PreToolUse` (Codex/Claude) = `BeforeTool` (Gemini)
2. **Post-tool hook**: `PostToolUse` (Codex/Claude) = `AfterTool` (Gemini)
3. **Prompt submitted**: `UserPromptSubmit` (Codex/Claude) = `BeforeAgent` (Gemini)
4. **Turn complete**: `Stop` (Codex/Claude) = `AfterAgent` (Gemini)
5. **Pre-compact**: `PreCompact` (Claude) = `PreCompress` (Gemini)

## Key Architectural Differences

### Codex CLI
- Minimal hook set (5 events)
- Focused on core interception points
- Hooks are opt-in via feature flag (still under development)
- Currently disabled on Windows

### Gemini CLI
- Adds model-layer hooks for request/response manipulation
- `BeforeModel`, `AfterModel`, `BeforeToolSelection` allow intercepting LLM calls directly
- Hooks enabled by default since v0.26.0
- Project-level hooks are fingerprinted for security

### Claude Code
- Most comprehensive hook system (23 events)
- Subagent orchestration hooks (`SubagentStart`, `SubagentStop`, `TeammateIdle`)
- File/config watching (`FileChanged`, `CwdChanged`, `ConfigChange`)
- Worktree management for isolated environments
- MCP elicitation hooks for server input requests
- Failure-specific hooks (`PostToolUseFailure`, `StopFailure`)
- Three handler types: `command`, `prompt`, `agent`

## Hook Handler Types

| Handler Type | Codex | Gemini | Claude |
|--------------|-------|--------|--------|
| Command (shell) | Yes | Yes | Yes |
| HTTP endpoint | — | — | Yes |
| Prompt (LLM eval) | — | — | Yes |
| Agent (multi-turn) | — | — | Yes |

## Common Input/Output Patterns

All three tools use similar patterns:
- **Input**: JSON on stdin with event-specific fields
- **Output**: Exit codes (0=allow, 2=block) or JSON response
- **Matchers**: Regex patterns to filter which tools/events trigger hooks
- **Timeout**: Configurable per-hook (default varies)

## Sources

- [Codex CLI Hooks](https://developers.openai.com/codex/hooks)
- [Gemini CLI Hooks Reference](https://geminicli.com/docs/hooks/reference/)
- [Claude Code Hooks Guide](https://code.claude.com/docs/en/hooks-guide)
- [Claude Code Hooks Reference](https://code.claude.com/docs/en/hooks)

---
*Last updated: 2026-03-31*
