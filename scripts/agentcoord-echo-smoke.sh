#!/usr/bin/env bash
# agentcoord-echo-smoke.sh — Wave B acceptance (1): the sonnet `coder` echo.
#
# Drives a real ctxloom session that delegates one turn to a configured agent
# and asserts the child echoes a marker phrase back through the coordinator's
# durable role mailbox — the whole B1 reach-back path end to end (spawn →
# coordinator MCP endpoint → agent_send → parent agent_recv).
#
#   scripts/agentcoord-echo-smoke.sh [--agent NAME] [--runtime host|container]
#
# The LIVE two-runtime run (host AND container) is performed by the coordinator
# or the user after review — this script is the named artifact that run uses,
# and its argument/plumbing logic is unit-tested
# (scripts/agentcoord-echo-smoke_test.sh). By default it reports the plan and
# exits 0 without launching an engine unless --live is passed, so it is safe to
# run in CI as a smoke of ITSELF.
set -euo pipefail

AGENT="coder"
RUNTIME="host"
MARKER="hello world via ctxloom"
LIVE=0
CTXLOOM="${CTXLOOM_BIN:-ctxloom}"

usage() {
	cat <<'USAGE'
agentcoord-echo-smoke.sh — the sonnet `coder` echo (Wave B acceptance 1)

  --agent NAME       configured ctxloom agent to delegate to (default: coder)
  --runtime AXIS     host | container (default: host)
  --marker TEXT      the phrase the child must echo back (default: "hello world via ctxloom")
  --live             actually launch the engine and assert the round-trip
                     (default: print the plan and exit 0 — a self-smoke)
  -h, --help         this help

The live round-trip:
  1. `ctxloom run` stands up the coordinator (durable stores + MCP endpoint +
     RunnerChannel) and injects CTXLOOM_COORD_URL / _CRED into the harness.
  2. The harness calls agent_run(agent=<AGENT>, prompt="echo back exactly: <MARKER>").
  3. The child forwards its ctxloom MCP tools to the coordinator (forward mode),
     runs, and agent_send(to:"parent") the marker.
  4. The parent agent_recv sees the marker → PASS.
USAGE
}

while [ $# -gt 0 ]; do
	case "$1" in
	--agent) AGENT="$2"; shift 2 ;;
	--runtime) RUNTIME="$2"; shift 2 ;;
	--marker) MARKER="$2"; shift 2 ;;
	--live) LIVE=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
	esac
done

case "$RUNTIME" in
host | container) ;;
*) echo "invalid --runtime $RUNTIME (want host|container)" >&2; exit 2 ;;
esac

# build_prompt is the unit-tested seam: the exact briefing the parent gives the
# child, which must be echoed verbatim.
build_prompt() {
	printf 'Immediately call agent_send(to:"parent", kind:"result", body:"%s") with that exact text, then stop.' "$1"
}

if [ "$LIVE" -ne 1 ]; then
	echo "agentcoord echo smoke (plan only; pass --live to run):"
	echo "  agent:   $AGENT"
	echo "  runtime: $RUNTIME"
	echo "  marker:  $MARKER"
	echo "  prompt:  $(build_prompt "$MARKER")"
	echo "OK (self-smoke; no engine launched)"
	exit 0
fi

# --- live path -------------------------------------------------------------
# The parent is itself a delegated coordinator: a headless `ctxloom run` whose
# first (and only) instruction is to spawn the child and wait for the echo. The
# transcript is scanned for the marker after the run completes.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
prompt="$(build_prompt "$MARKER")"
coordinator_brief="Call agent_run(agent:\"$AGENT\", prompt:\"$prompt\"). Then call agent_recv (wait:120) and print any message body you receive. Then stop."

echo "launching: $CTXLOOM run --agent $AGENT --runtime $RUNTIME --print ..." >&2
out="$("$CTXLOOM" run --print --runtime "$RUNTIME" "$coordinator_brief" 2>"$work/err" || true)"

if printf '%s' "$out" | grep -qF "$MARKER"; then
	echo "PASS: the child echoed the marker back through the coordinator ($RUNTIME runtime)"
	exit 0
fi
echo "FAIL: the marker never round-tripped ($RUNTIME runtime)" >&2
echo "--- stdout ---" >&2; printf '%s\n' "$out" >&2
echo "--- stderr ---" >&2; cat "$work/err" >&2
exit 1
