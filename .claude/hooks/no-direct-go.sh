#!/usr/bin/env bash
# Block direct `go build`/`go test`/`go install`/`go run` and redirect to `just` targets.
# See: justfile for the canonical build/test/install/run pipelines.

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

# `(^|[^[:alnum:]_])go[[:space:]]+<sub>([^[:alnum:]_]|$)` — word-boundary match so
# `cargo build`, `mango run`, etc. don't trigger; `cd x && go build` does.
match() { [[ "$cmd" =~ (^|[^[:alnum:]_])go[[:space:]]+$1([^[:alnum:]_]|$) ]]; }

if match build;   then deny "Use \`just build\` instead of \`go build\` — the justfile is the canonical build pipeline."; fi
if match test;    then deny "Use \`just test\` instead of \`go test\` — the justfile is the canonical test pipeline."; fi
if match install; then deny "Use \`just install\` instead of \`go install\` — the justfile is the canonical install pipeline."; fi
if match run;     then deny "Use \`just run -- <args>\` instead of \`go run\` — the justfile is the canonical run target."; fi

exit 0
