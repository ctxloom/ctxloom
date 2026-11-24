#!/usr/bin/env bash
# Self-test of scripts/mcp-env-probe.sh against a fake claude and a fake
# ctxloom (no model call, no token of anyone's): the probe gives everything it
# starts a temporary ctxloom home, HOME and project of its own, never the
# user's ~/.ctxloom or ~/.ctxloom-rs, and stops the daemon of that home before
# it removes it.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
probe=$here/mcp-env-probe.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL mcp-env-probe.selftest: $*" >&2; exit 1; }
command -v jq >/dev/null || { echo "SKIP mcp-env-probe.selftest: no jq on PATH" >&2; exit 0; }

mkdir -p "$tmp/bin" "$tmp/tmp" "$tmp/user"
# claude: starts the MCP config's server, as claude starts a stdio server.
cat > "$tmp/bin/claude" <<'FAKE'
#!/bin/sh
[ "$1" = "--version" ] && { echo "fake"; exit 0; }
while [ $# -gt 0 ]; do
    [ "$1" = "--mcp-config" ] && config=$2
    shift
done
command=$(jq -r '.mcpServers.ctxloom.command' "$config")
"$command" < /dev/null
FAKE
# ctxloom: says what it was asked and in which home.
cat > "$tmp/ctxloom" <<FAKE
#!/bin/sh
echo "\$*|CTXLOOM_DAEMON_ROOT=\$CTXLOOM_DAEMON_ROOT|HOME=\$HOME|ROOT=\$CTXLOOM_ROOT|EXISTS=\$([ -d "\$CTXLOOM_DAEMON_ROOT" ] && echo yes)" >> "$tmp/ctxloom.log"
FAKE
chmod 0755 "$tmp/bin/claude" "$tmp/ctxloom"

out=$(HOME="$tmp/user" TMPDIR="$tmp/tmp" PATH="$tmp/bin:$PATH" \
    CTXLOOM_DAEMON_ROOT="$tmp/user/.ctxloom-rs" CLAUDE_CODE_OAUTH_TOKEN=fake \
    "$probe" "$tmp/ctxloom" 2>&1) || fail "the probe failed: $out"

home=$(printf '%s\n' "$out" | sed -n 's/^CTXLOOM_DAEMON_ROOT: //p')
case "$home" in
    "$tmp/tmp/mcp-env-probe."*/home) ;;
    *) fail "the server's CTXLOOM_DAEMON_ROOT is not the probe's own: '$home' ($out)" ;;
esac
grep -q "^HOME: $home\$" <<< "$out" || fail "the server's HOME is not the probe's: $out"
serve=$(grep '^mcp serve|' "$tmp/ctxloom.log") || fail "the server never ran: $(cat "$tmp/ctxloom.log")"
[ "$serve" = "mcp serve|CTXLOOM_DAEMON_ROOT=$home|HOME=$home|ROOT=${home%/home}/project|EXISTS=yes" ] \
    || fail "the server ran as: $serve"
stop=$(grep '^daemon stop|' "$tmp/ctxloom.log") || fail "the daemon was not stopped: $(cat "$tmp/ctxloom.log")"
[ "$stop" = "daemon stop|CTXLOOM_DAEMON_ROOT=$home|HOME=$home|ROOT=${home%/home}/project|EXISTS=yes" ] \
    || fail "stopped as: $stop (the home must still be there)"
[ ! -e "${home%/home}" ] || fail "the probe's directory is left"
[ ! -e "$tmp/user/.ctxloom" ] && [ ! -e "$tmp/user/.ctxloom-rs" ] || fail "the user's home was touched"
echo "ok   mcp-env-probe.selftest"
