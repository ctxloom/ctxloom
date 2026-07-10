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
check_contains "plan builds the echo prompt" 'agent_send(to_role:"parent"' "$out"

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

if [ "$fails" -ne 0 ]; then
	echo "$fails test(s) failed"
	exit 1
fi
echo "all echo-smoke unit tests passed"
