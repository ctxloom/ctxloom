#!/usr/bin/env bash
# Resolves the latest PUBLISHED version of one LLM-engine CLI from its
# release feed. Used by .github/workflows/engine-drift-detect.yml (Stage 1,
# "detect", of the self-healing engine-format pipeline) to compare against
# the tested-version lock in .github/engine-versions.env. Read-only: makes no
# changes to the repo or to any engine account, and needs no engine
# credential — only the ambient GH_TOKEN (repo-scoped GITHUB_TOKEN, already
# required for `gh` to talk to the GitHub API without hitting the
# unauthenticated rate limit).
#
# One case per engine, matching the plan's web-verified per-engine
# version-detect table (self-healing-engine-pipeline plan, Stage 1 / the
# Grounding table):
#   codex        -> npm view @openai/codex version
#   claude-code  -> npm view @anthropic-ai/claude-code version
set -euo pipefail

engine="${1:?usage: detect-engine-version.sh <codex|claude-code>}"

case "$engine" in
codex)
  npm view @openai/codex version
  ;;
claude-code)
  npm view @anthropic-ai/claude-code version
  ;;
*)
  echo "detect-engine-version.sh: unknown engine '$engine'" >&2
  exit 1
  ;;
esac
