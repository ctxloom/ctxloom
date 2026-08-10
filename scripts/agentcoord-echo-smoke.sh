#!/usr/bin/env bash
# agentcoord-echo-smoke.sh — Wave B acceptance (1): the sonnet `coder` echo.
#
# Drives a real ctxloom session that delegates one turn to a configured agent
# and asserts the child echoes a marker phrase back through the coordinator's
# durable role mailbox — the whole reach-back path end to end (spawn → runner
# unix-socket MCP → RunChannel plane-2 → durable mailbox → parent agent_recv).
#
#   scripts/agentcoord-echo-smoke.sh [--agent NAME] [--runtime host|container]
#
# PASS is decided from the coordinator's own durable journals (runs.jsonl +
# mailbox.jsonl under its state dir), never by grepping the parent's stdout:
# the coordinator brief handed to the parent embeds the marker verbatim, so
# the parent's own narration ("I will call agent_send with text: <marker>")
# can false-pass a run where the child never actually round-tripped anything
# (this happened live during C4). Requires `jq`.
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

The live round-trip (B1.6 runner-terminated topology):
  1. `ctxloom run` stands up the runtime coordinator (durable stores + gRPC
     RunnerChannel/RunChannel) and stamps CTXLOOM_COORD_URL / _CRED onto the
     RUNNER's spawn env; the runner serves MCP on a local unix socket the
     harness's stdio shim forwards to (CTXLOOM_MCP_SOCKET).
  2. The harness calls agent_run(role=<AGENT>, input.prompt="echo ... <MARKER>").
  3. The child's runner turns its agent_send(to_role:"parent") into a typed
     plane-2 PeerSendRequest back to the coordinator.
  4. PASS is read back from the coordinator's own journals: runs.jsonl gives
     the child's harp (the run.enqueued fact for <AGENT> carrying <MARKER> in
     its journaled prompt), then mailbox.jsonl must carry a mail.queued FROM
     that harp with <MARKER> in its body — the parent's stdout is never
     trusted, since its own briefing already contains <MARKER> verbatim.
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

# Make the marker unique to THIS invocation (timestamp + pid + $RANDOM): the
# journal oracle below matches on marker CONTAINMENT in a journal that can
# carry facts from earlier runs (this script, or another coordinator on this
# machine — concurrent coordinators are routine here), so a stale fact must
# never be able to satisfy a fresh run's PASS. Applied even when the caller
# supplies --marker.
MARKER="${MARKER} $(date +%s)-$$-${RANDOM}"

# build_prompt is the unit-tested seam: the exact briefing the parent gives the
# child, which must be echoed verbatim.
# Phrased as a legitimate coordinator task: a bare "immediately call
# agent_send(...)" briefing reads as a prompt-injection to safety-conscious
# child models (a live sonnet REFUSED it), while a self-consistent
# connectivity-check framing passes.
build_prompt() {
	printf 'You are a delegated child session; your coordinator spawned you as a connectivity check. Please confirm the reach-back path works: call the agent_send tool with to_role set to parent and text set to: %s. Then finish.' "$1"
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
# first (and only) instruction is to spawn the child and wait for the echo.
# PASS is decided from the coordinator's own journals after the run completes
# — see the journal-oracle block below the run.
#
# Artifacts ($work/stdout.log, $work/stderr.log) are DELETED only on PASS; a
# FAIL keeps the directory and prints its path — a cleaned-up failure once
# destroyed the only stderr trail of a dead child.
if ! command -v jq >/dev/null 2>&1; then
	echo "agentcoord-echo-smoke.sh --live requires jq (journal-oracle PASS check); install it and retry" >&2
	exit 2
fi
work="$(mktemp -d)"
smoke_status=1
cleanup() {
	if [ "$smoke_status" -eq 0 ]; then
		rm -rf "$work"
	else
		echo "artifacts preserved in $work (stdout.log, stderr.log)" >&2
	fi
}
trap cleanup EXIT
prompt="$(build_prompt "$MARKER")"
coordinator_brief="Call agent_run(role:\"$AGENT\", input:{prompt:\"$prompt\"}). Then call agent_recv (wait:120) and print any message text you receive. Then stop."

# CTXLOOM_VERBOSE=1 turns on the CHILD-side launch diagnostics: the
# coordinator's spawner forwards the child `llm serve` plugin's stderr (which
# carries the ACP adapter's stderr) through its own process stderr. For the
# --one-shot topology that process is the parent engine's stdio `ctxloom mcp serve`,
# so the trail lands in the engine's MCP server logs (claude:
# ~/.cache/claude-cli-nodejs/<project>/mcp-logs-ctxloom/).
export CTXLOOM_VERBOSE=1

# mtime_epoch prints a file's mtime as a unix epoch (GNU or BSD stat).
mtime_epoch() {
	stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null
}

# journal_candidates prints (one per line) every coordinator state dir with a
# runs.jsonl whose mtime is >= since: the project state dir this run's
# coordinator normally owns (~/.ctxloom/coord/<key>/), PLUS the ephemeral
# mktemp fallback (${TMPDIR:-/tmp}/ctxloom-coord-*/) it takes instead when
# another live coordinator already owns that project's journals — which
# happens routinely on a machine with concurrent coordinators. No CLI prints
# the state dir a given run used, so both locations are always swept.
journal_candidates() {
	since="$1"
	for d in "$HOME/.ctxloom/coord"/*/ "${TMPDIR:-/tmp}"/ctxloom-coord-*/; do
		[ -d "$d" ] || continue
		runs_f="${d%/}/runs.jsonl"
		[ -f "$runs_f" ] || continue
		mt="$(mtime_epoch "$runs_f")"
		[ -n "$mt" ] || continue
		# -ge, not -gt: a fast run can complete within the same mtime second
		# as $run_start.
		[ "$mt" -ge "$since" ] && printf '%s\n' "${d%/}"
	done
}

# The child's runtime rides the AGENT definition (`ctxloom run` has no
# runtime flag); --runtime here only names which axis this invocation is
# accepting — the caller picks an agent whose runtime matches.
run_start="$(($(date +%s) - 1))" # -1s: mtime-vs-date(1) granularity slack
echo "launching: $CTXLOOM run --one-shot <coordinator brief spawning agent=$AGENT> (child runtime axis: $RUNTIME)" >&2
out="$("$CTXLOOM" run --one-shot "$coordinator_brief" 2>"$work/stderr.log" || true)"
printf '%s\n' "$out" >"$work/stdout.log"

# --- journal oracle ----------------------------------------------------------
# PASS requires TWO facts, never the parent's narration:
#   1. runs.jsonl: a run.enqueued fact for this AGENT whose journaled prompt
#      carries the unique MARKER → gives the child's harp.
#   2. mailbox.jsonl (same dir): a mail.queued fact FROM that harp (never
#      "user", the viewer-injection sender) whose body carries MARKER — the
#      child's OWN agent_send, i.e. the actual round-trip.
checked=()
child_harp=""
pass_dir=""
pass_harp=""
for dir in $(journal_candidates "$run_start"); do
	checked+=("$dir")
	harp="$(jq -r --arg agent "$AGENT" --arg marker "$MARKER" '
		select(.kind == "run.enqueued")
		| select(.data.agent == $agent)
		| select((.data.prompt // "") | contains($marker))
		| .data.harp
	' "$dir/runs.jsonl" 2>/dev/null | tail -n1)"
	[ -n "$harp" ] || continue
	child_harp="$harp"
	[ -f "$dir/mailbox.jsonl" ] || continue
	if jq -e --arg harp "$harp" --arg marker "$MARKER" '
		select(.kind == "mail.queued")
		| select(.data.from == $harp)
		| select(.data.from != "user")
		| select((.data.body // "") | contains($marker))
	' "$dir/mailbox.jsonl" >/dev/null 2>&1; then
		pass_dir="$dir"
		pass_harp="$harp"
		break
	fi
done

if [ -n "$pass_dir" ]; then
	echo "PASS: child $pass_harp echoed the marker back through the coordinator's mailbox journal ($RUNTIME runtime; $pass_dir)"
	smoke_status=0
	exit 0
fi

echo "FAIL: the marker never round-tripped through the coordinator's journals ($RUNTIME runtime)" >&2
if [ "${#checked[@]}" -eq 0 ]; then
	echo "  no coordinator journal dir found with runs.jsonl newer than the run's start ($run_start)" >&2
	echo "  looked under: \$HOME/.ctxloom/coord/*/ and ${TMPDIR:-/tmp}/ctxloom-coord-*/" >&2
elif [ -z "$child_harp" ]; then
	echo "  no run.enqueued fact matched agent=\"$AGENT\" + marker in runs.jsonl under:" >&2
	printf '    %s\n' "${checked[@]}" >&2
else
	echo "  found child harp \"$child_harp\" but no mail.queued FROM it carrying the marker in mailbox.jsonl under:" >&2
	printf '    %s\n' "${checked[@]}" >&2
fi
echo "--- stdout ($work/stdout.log) ---" >&2; printf '%s\n' "$out" >&2
echo "--- stderr ($work/stderr.log) ---" >&2; cat "$work/stderr.log" >&2
exit 1
