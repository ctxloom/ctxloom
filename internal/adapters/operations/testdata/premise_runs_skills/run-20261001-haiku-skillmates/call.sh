#!/bin/zsh -i
# call.sh <prompt-file> <out-file>: one bare, isolated selector call. Throwaway
# HOME/CLAUDE_CONFIG_DIR and an empty cwd (no CLAUDE.md, no .mcp.json), no tools,
# no MCP; the token is read from the interactive env and never printed.
P=${1:A}; O=${2:A}
T=$(mktemp -d)
mkdir -p $T/.claude $T/cwd
cd $T/cwd
env -i HOME=$T CLAUDE_CONFIG_DIR=$T/.claude PATH="$HOME/.local/bin:/usr/bin:/bin" CLAUDE_CODE_OAUTH_TOKEN="$CLAUDE_CODE_OAUTH_TOKEN" \
  timeout 100 claude --print --model claude-haiku-4-5-20251001 --tools "" --strict-mcp-config < "$P" > "$O" 2>"$O.err"
rc=$?
cd /; rm -rf $T
exit $rc
