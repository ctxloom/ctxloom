#!/usr/bin/env bash
# Unit test for agentcoord-echo-smoke.sh — exercises its argument handling and
# the prompt-building seam WITHOUT launching an engine (the live round-trip is
# performed by the coordinator/user after review, per Wave B acceptance 1).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/agentcoord-echo-smoke.sh"
fails=0

check() {
	local name="$1" want="$2" got="$3"
	if [ "$got" = "$want" ]; then
		echo "ok   - $name"
	else
		echo "FAIL - $name: want [$want] got [$got]"
		fails=$((fails + 1))
	fi
}

check_contains() {
	local name="$1" needle="$2" hay="$3"
	if printf '%s' "$hay" | grep -qF "$needle"; then
		echo "ok   - $name"
	else
		echo "FAIL - $name: [$hay] missing [$needle]"
		fails=$((fails + 1))
	fi
}

# 1. Self-smoke (no --live) exits 0 and prints the plan with the defaults.
out="$(bash "$script")"
rc=$?
check "self-smoke exit code" "0" "$rc"
check_contains "default agent in plan" "agent:   coder" "$out"
check_contains "default runtime in plan" "runtime: host" "$out"
check_contains "default marker in plan" "hello world via ctxloom" "$out"
check_contains "plan builds the echo prompt" 'call the agent_send tool with to_role set to parent' "$out"

# 2. Flags flow into the plan.
out="$(bash "$script" --agent reviewer --runtime container --marker "PONG-42")"
check_contains "custom agent" "agent:   reviewer" "$out"
check_contains "custom runtime" "runtime: container" "$out"
check_contains "custom marker echoed in prompt" "PONG-42" "$out"

# 3. An invalid runtime is rejected (exit 2).
set +e
bash "$script" --runtime bogus >/dev/null 2>&1
rc=$?
set -e
check "invalid runtime exit code" "2" "$rc"

# 4. An unknown flag is rejected (exit 2).
set +e
bash "$script" --nope >/dev/null 2>&1
rc=$?
set -e
check "unknown flag exit code" "2" "$rc"

# 5. --help exits 0.
set +e
bash "$script" --help >/dev/null 2>&1
rc=$?
set -e
check "help exit code" "0" "$rc"

# 6. The journal oracle, driven against fixture journals. Each decoy below is
#    a fact the coordinator really writes that must NOT pass, one per
#    predicate the oracle holds.
if command -v jq >/dev/null 2>&1; then
	# shellcheck source=agentcoord-echo-smoke.sh
	source "$script"
	j="$(mktemp -d)"
	trap 'rm -rf "$j"' EXIT
	audit() { # actor from kind
		printf '{"kind":"interaction","at":"2026-01-01T00:00:00Z","data":{"kind":"spool_mail_out","actor":"%s","detail":{"message_id":"m1","from":"%s","kind":"%s","ref":"%s:in/x.md"}}}\n' "$1" "$2" "$3" "$1"
	}
	printf '%s\n' \
		'{"kind":"run.enqueued","at":"2026-01-01T00:00:00Z","data":{"harp":"kid-a","agent":"coder","parent_harp":"dad-a","prompt":"echo MARK-1 now"}}' \
		'{"kind":"run.enqueued","at":"2026-01-01T00:00:00Z","data":{"harp":"kid-b","agent":"reviewer","parent_harp":"dad-a","prompt":"echo MARK-1 now"}}' \
		>"$j/runs.jsonl"

	check "enqueued child and parent by agent + marker" "kid-a dad-a" "$(enqueued_child "$j" coder MARK-1)"
	check "another agent's run is not this child" "kid-b dad-a" "$(enqueued_child "$j" reviewer MARK-1)"
	check "a prompt without the marker matches nothing" "" "$(enqueued_child "$j" coder MARK-2)"

	oracle() { # name want(pass|fail) child parent
		local got=fail
		if routed_to_parent "$j" "$3" "$4"; then got=pass; fi
		check "$1" "$2" "$got"
	}
	oracle "no audit journal is no proof" fail kid-a dad-a

	audit dad-a kid-a error >"$j/interactions.jsonl"
	audit kid-a kid-a error >>"$j/interactions.jsonl"
	audit kid-a kid-a message >>"$j/interactions.jsonl"
	audit dad-a kid-b message >>"$j/interactions.jsonl"
	audit dad-a "" message >>"$j/interactions.jsonl"
	# agent_send is audited BEFORE the write is attempted: only spool_mail_out
	# records a write that happened.
	audit dad-a kid-a message | sed 's/"spool_mail_out"/"agent_send"/' >>"$j/interactions.jsonl"
	oracle "an attempted send is not a written one" fail kid-a dad-a
	oracle "drop notice (kind error) from the child is not the echo" fail kid-a dad-a
	oracle "mail into the child's own inbox is not the echo" fail kid-a dad-a
	oracle "a sibling's send to the same parent is not the echo" fail kid-c dad-a

	audit dad-a kid-a result >>"$j/interactions.jsonl"
	oracle "the child's own send written into its parent's inbox" pass kid-a dad-a
	oracle "the same write does not prove a different parent" fail kid-a dad-b
else
	echo "skip - journal oracle (jq not installed)"
fi

if [ "$fails" -ne 0 ]; then
	echo "$fails test(s) failed"
	exit 1
fi
echo "all echo-smoke unit tests passed"
