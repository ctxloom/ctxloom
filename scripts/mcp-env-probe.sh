#!/usr/bin/env bash
# OD-7's live probe (daemon slice 3, ruled 2026-11-13): does claude pass the
# environment it runs in to a stdio MCP server it starts, so that ctxloom's
# own server (`ctxloom mcp serve`) gets the session's name (CTXLOOM_SESSION)
# and ctxloom's home (CTXLOOM_HOME), which its entry in the MCP config never
# carries? One REAL claude turn, authenticated by an environment token only.
#
#   CLAUDE_CODE_OAUTH_TOKEN=... scripts/mcp-env-probe.sh [ctxloom binary]
#
# The token is the only credential used: the user's own ~/.claude login is
# never read. claude runs with a fresh, temporary CLAUDE_CONFIG_DIR, the
# probe's MCP config alone (--strict-mcp-config), and only the one tool
# allowed. The server's entry runs a wrapper that records the environment it
# was started with, then execs `ctxloom mcp serve`. The turn asks for one
# `session_list` call (a read: a daemon of ctxloom's home answers it, started
# if none serves). The probe prints what the server received and whether the
# tool answered; nothing it writes outlives it but that daemon.
set -euo pipefail

if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    echo "mcp-env-probe: CLAUDE_CODE_OAUTH_TOKEN is not set; the probe authenticates by an environment token only, never your login" >&2
    exit 2
fi
command -v claude >/dev/null || { echo "mcp-env-probe: no claude on PATH" >&2; exit 2; }
command -v jq >/dev/null || { echo "mcp-env-probe: no jq on PATH" >&2; exit 2; }

top=$(cd "$(dirname "$0")/.." && pwd)
ctxloom=${1:-$top/target/debug/ctxloom}
[ -x "$ctxloom" ] || { echo "mcp-env-probe: no ctxloom binary at $ctxloom (just build)" >&2; exit 2; }

work=$(mktemp -d "${TMPDIR:-/tmp}/mcp-env-probe.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/config" "$work/project"

# The server as an engine starts it, its environment recorded first.
cat > "$work/server.sh" <<EOF
#!/bin/sh
env | sort > "$work/server.env"
exec "$ctxloom" mcp serve
EOF
chmod 0755 "$work/server.sh"

# ctxloom's entry, as the session home writes it: a command, NO env.
jq -n --arg cmd "$work/server.sh" \
    '{mcpServers: {ctxloom: {command: $cmd, args: []}}}' > "$work/mcp.json"

session="projects/probe-mcp-env/sessions/probe-mcp-turn"
(
    cd "$work/project"
    env -u ANTHROPIC_API_KEY -u ANTHROPIC_AUTH_TOKEN \
        CLAUDE_CONFIG_DIR="$work/config" \
        CTXLOOM_SESSION="$session" \
        CTXLOOM_PROBE_MARKER="present" \
        claude -p "Call the ctxloom tool session_list exactly once with no arguments, then reply with the single word DONE." \
            --mcp-config "$work/mcp.json" --strict-mcp-config \
            --allowedTools mcp__ctxloom__session_list \
            --output-format stream-json --verbose \
            < /dev/null > "$work/stream.jsonl" 2> "$work/claude.err"
) || echo "mcp-env-probe: claude exited $? (stderr below)"

echo "== claude ($(claude --version 2>/dev/null || echo unknown))"
echo "== the environment the stdio server was started with"
if [ -f "$work/server.env" ]; then
    for name in CTXLOOM_SESSION CTXLOOM_HOME CTXLOOM_PROBE_MARKER CLAUDE_CODE_OAUTH_TOKEN HOME PATH; do
        if grep -q "^$name=" "$work/server.env"; then
            case "$name" in
                CLAUDE_CODE_OAUTH_TOKEN) echo "$name: present (value withheld)" ;;
                *) echo "$name: $(grep "^$name=" "$work/server.env" | cut -d= -f2-)" ;;
            esac
        else
            echo "$name: ABSENT"
        fi
    done
    echo "variables in all: $(wc -l < "$work/server.env")"
else
    echo "the server was never started"
fi
echo "== the tool call"
jq -c 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use") | {name, input}' "$work/stream.jsonl" 2>/dev/null || true
jq -c 'select(.type == "user") | .message.content[]? | select(.type == "tool_result") | {is_error, content}' "$work/stream.jsonl" 2>/dev/null | cut -c1-400 || true
jq -c 'select(.type == "result") | {subtype, is_error, result}' "$work/stream.jsonl" 2>/dev/null || true
if [ -s "$work/claude.err" ]; then
    echo "== claude's stderr"
    head -20 "$work/claude.err"
fi
