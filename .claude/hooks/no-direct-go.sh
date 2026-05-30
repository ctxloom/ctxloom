#!/usr/bin/env bash
# Block direct `go build`/`go test`/`go install`/`go run` and `lizard`, redirecting
# to `just` targets.
# See: justfile for the canonical build/test/install/run/complexity pipelines.

set -euo pipefail

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // empty')
[[ -z "$cmd" ]] && exit 0

deny() {
  jq -n --arg reason "$1" '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: $reason
    }
  }'
  exit 0
}

# `^[[:space:]]*go[[:space:]]+<sub>([^[:alnum:]_]|$)` — anchored to the START of the
# command, so a leading `go build` is blocked but `cargo build`, `grep "go test"`,
# `echo go run`, or a mid-command `cd x && go build` pass through untouched.
match() { [[ "$cmd" =~ ^[[:space:]]*go[[:space:]]+$1([^[:alnum:]_]|$) ]]; }

if match build;   then deny "Use \`just build\` instead of \`go build\` — the justfile is the canonical build pipeline."; fi
if match test;    then deny "Use \`just test\` instead of \`go test\` — the justfile is the canonical test pipeline."; fi
if match install; then deny "Use \`just install\` instead of \`go install\` — the justfile is the canonical install pipeline."; fi
if match run;     then deny "Use \`just run -- <args>\` instead of \`go run\` — the justfile is the canonical run target."; fi

# `lizard` (cyclomatic-complexity analyzer). Anchored to the START of the command
# only, so a leading `lizard …` invocation is blocked but `grep lizard`,
# `cat f | lizard`, `echo lizard`, or `--lizard` flags pass through untouched.
if [[ "$cmd" =~ ^[[:space:]]*lizard([[:space:]]|$) ]]; then
  deny "Use \`just complexity\` (or \`just complexity-csv\`) instead of \`lizard\` — the justfile is the canonical complexity report."
fi

exit 0
