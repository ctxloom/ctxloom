#!/usr/bin/env bash
# agentcoord-echo-smoke.sh — Wave B acceptance (1): the sonnet `coder` echo.
#
# Drives a real ctxloom session that delegates one turn to a configured agent
# and asserts the child's agent_send reaches its parent through the
# coordinator — the whole reach-back path end to end (spawn → the child's
# agent_send written to its own spool out/ → the coordinator routes it → one
# file in the parent's spool in/).
#
#   scripts/agentcoord-echo-smoke.sh [--agent NAME] [--runtime host|container]
#
# PASS is decided from the coordinator's own durable journals (runs.jsonl +
# interactions.jsonl under its state dir), never by grepping the parent's
# stdout: the coordinator brief handed to the parent embeds the marker
# verbatim, so the parent's own narration ("I will call agent_send with text:
# <marker>") can false-pass a run where the child never actually
# round-tripped anything (this happened live during C4). Requires `jq`.
#
# The proof is the audit journal, not the spool file the message travelled
# in: a delivered spool file may be deleted once its reader acknowledges it,
# so it is not evidence that survives the round trip it would prove. The
# price is that the marker's TEXT is not re-read on the way back — the
# marker binds the child to this invocation (its journaled prompt), and the
# audit proves that child's own send was written into its parent's inbox.
#
# The LIVE two-runtime run (host AND container) is performed by the coordinator
# or the user after review — this script is the named artifact that run uses,
# and its argument handling and journal oracle are unit-tested
# (scripts/agentcoord-echo-smoke_test.sh, which sources this file). By default
# it reports the plan and exits 0 without launching an engine unless --live is
# passed, so it is safe to run in CI as a smoke of ITSELF.
set -euo pipefail

# build_prompt is the unit-tested seam: the exact briefing the parent gives the
# child, which must be echoed verbatim.
# Phrased as a legitimate coordinator task: a bare "immediately call
# agent_send(...)" briefing reads as a prompt-injection to safety-conscious
# child models (a live sonnet REFUSED it), while a self-consistent
# connectivity-check framing passes.
build_prompt() {
	printf 'You are a delegated child session; your coordinator spawned you as a connectivity check. Please confirm the reach-back path works: call the agent_send tool with to_role set to parent and text set to: %s. Then finish.' "$1"
}

# --- journal oracle ----------------------------------------------------------
# PASS requires TWO facts from one coordinator state dir, never the parent's
# narration:
#   1. enqueued_child: runs.jsonl's run.enqueued fact for AGENT whose
#      journaled prompt carries the unique MARKER → the child's harp and its
#      parent's harp.
#   2. routed_to_parent: an interactions.jsonl spool_mail_out entry (audited by
#      the coordinator's mail courier only AFTER the file is written into the
#      recipient's in/) whose actor is that parent and whose `from` is that
#      child. `from` is the identity the coordinator resolved from the spool
#      directory the message was found in, never the file's own claim — so it
#      is the child's OWN agent_send.
#      The kind is pinned to what a child's own send can carry (message,
#      result, question): the coordinator also writes notices FROM a child TO
#      its parent on the child's behalf — an `error` quoting an undeliverable
#      message (noticeSpoolDrop) among them — and none of those is the echo.

# enqueued_child DIR AGENT MARKER prints "<child-harp> <parent-harp>" for the
# newest matching run.enqueued fact in DIR/runs.jsonl, or nothing.
enqueued_child() {
	{ jq -r --arg agent "$2" --arg marker "$3" '
		select(.kind == "run.enqueued")
		| select(.data.agent == $agent)
		| select((.data.prompt // "") | contains($marker))
		| "\(.data.harp) \(.data.parent_harp // "")"
	' "$1/runs.jsonl" 2>/dev/null || true; } | tail -n1
}

# routed_to_parent DIR CHILD PARENT succeeds iff DIR/interactions.jsonl records
# the coordinator writing CHILD's own send into PARENT's inbox.
routed_to_parent() {
	[ -f "$1/interactions.jsonl" ] || return 1
	jq -e --arg child "$2" --arg parent "$3" '
		select(.kind == "interaction")
		| .data
		| select(.kind == "spool_mail_out")
		| select(.actor == $parent)
		| select(.detail.from == $child)
		| select(.detail.kind == "message" or .detail.kind == "result" or .detail.kind == "question")
	' "$1/interactions.jsonl" >/dev/null 2>&1
}

# Sourced (by the unit test) for the functions above: stop before the CLI.
if [ "${BASH_SOURCE[0]}" != "$0" ]; then
	return 0
fi

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
     RUNNER's spawn env; the runner serves the session's MCP endpoint
     (URL + bearer in the session's own registry), which the engine dials.
  2. The harness calls agent_run(role=<AGENT>, input.prompt="echo ... <MARKER>").
  3. The child's runner writes its agent_send(to_role:"parent") as a file in
     the child's own spool out/; the coordinator routes it and writes ONE file
     into the parent's spool in/.
  4. PASS is read back from the coordinator's own journals: runs.jsonl gives
     the child's harp and its parent's (the run.enqueued fact for <AGENT>
     carrying <MARKER> in its journaled prompt), then interactions.jsonl must
     record a spool_mail_out to that parent FROM that child, of a kind a
     child's own send carries — the parent's stdout is never trusted, since
     its own briefing already contains <MARKER> verbatim.
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
# journal oracle matches on marker CONTAINMENT in a journal that carries facts
# from earlier runs of this project's coordinator, so a stale fact must never
# be able to satisfy a fresh run's PASS. Applied even when the caller supplies
# --marker.
MARKER="${MARKER} $(date +%s)-$$-${RANDOM}"

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
# — see the journal oracle above.
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
coordinator_brief="Call agent_run(role:\"$AGENT\", input:{prompt:\"$prompt\"}). Then stop: the child's reply reaches your inbox on its own, and this run judges it from the journals."

# CTXLOOM_VERBOSE=1 turns on the CHILD-side launch diagnostics: the
# coordinator's spawner forwards the child runner's stderr through its own
# process stderr.
export CTXLOOM_VERBOSE=1

# mtime_epoch prints a file's mtime as a unix epoch (GNU or BSD stat).
mtime_epoch() {
	stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null
}

# journal_candidates prints (one per line) every coordinator project state dir
# (~/.ctxloom/coord/<key>/) with a runs.jsonl whose mtime is >= since. No CLI
# prints the state dir a given run used, so every project's is swept; the
# unique marker is what picks this run's facts out of them.
journal_candidates() {
	since="$1"
	for d in "$HOME/.ctxloom/coord"/*/; do
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

checked=()
child_harp=""
parent_harp=""
pass_dir=""
for dir in $(journal_candidates "$run_start"); do
	checked+=("$dir")
	pair="$(enqueued_child "$dir" "$AGENT" "$MARKER")"
	[ -n "$pair" ] || continue
	child_harp="${pair%% *}"
	parent_harp="${pair#* }"
	[ -n "$parent_harp" ] || continue
	if routed_to_parent "$dir" "$child_harp" "$parent_harp"; then
		pass_dir="$dir"
		break
	fi
done

if [ -n "$pass_dir" ]; then
	echo "PASS: child $child_harp's own send was written into its parent $parent_harp's inbox by the coordinator ($RUNTIME runtime; $pass_dir)"
	smoke_status=0
	exit 0
fi

echo "FAIL: the marker never round-tripped through the coordinator's journals ($RUNTIME runtime)" >&2
if [ "${#checked[@]}" -eq 0 ]; then
	echo "  no coordinator journal dir found with runs.jsonl newer than the run's start ($run_start)" >&2
	echo "  looked under: \$HOME/.ctxloom/coord/*/" >&2
elif [ -z "$child_harp" ]; then
	echo "  no run.enqueued fact matched agent=\"$AGENT\" + marker in runs.jsonl under:" >&2
	printf '    %s\n' "${checked[@]}" >&2
elif [ -z "$parent_harp" ]; then
	echo "  child harp \"$child_harp\" was enqueued with no parent_harp, so it has no parent to echo to; under:" >&2
	printf '    %s\n' "${checked[@]}" >&2
else
	echo "  found child \"$child_harp\" (parent \"$parent_harp\") but no spool_mail_out to that parent FROM that child in interactions.jsonl under:" >&2
	printf '    %s\n' "${checked[@]}" >&2
fi
echo "--- stdout ($work/stdout.log) ---" >&2; printf '%s\n' "$out" >&2
echo "--- stderr ($work/stderr.log) ---" >&2; cat "$work/stderr.log" >&2
exit 1
