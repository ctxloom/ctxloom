mcp-server-no-session = ctxloom's MCP server serves a session, and { $variable } names none
mcp-server-no-session-fix = an engine ctxloom launched starts it; nobody runs `ctxloom mcp serve` by hand
mcp-server-failed = ctxloom's MCP server could not serve: { $ext_err_txt }
mcp-server-argument-missing = { $tool } needs its { $argument } argument
mcp-server-unknown-tool = ctxloom's MCP server offers no tool { $tool }
mcp-server-urgency-invalid = { $given } is no urgency: inform, wake or steer
# Step 8 U4 (texts.md §2, D14), said to the model in a deny: accepted verbatim (owner, 2026-11-16; reviewed before release).
mcp-server-approval-expired = No one answered this permission request within { $timeout }, so it was denied. Do not retry it unasked; tell the user what you wanted to run and why, and go on without it.
mcp-server-approval-denied-at-once = This session denies every permission request at once (engines.{ $engine }.approval_timeout is now). Do not retry it; tell the user what you wanted to run and why, and go on without it.
mcp-server-approval-denied = The user denied this tool call. Do not retry it unasked; ask the user how to go on.
