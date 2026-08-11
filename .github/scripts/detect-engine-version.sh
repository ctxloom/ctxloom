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
#   kiro         -> kiro.dev/changelog/cli/ scrape (no releases API exists)
set -euo pipefail

engine="${1:?usage: detect-engine-version.sh <codex|claude-code|kiro>}"

case "$engine" in
codex)
  npm view @openai/codex version
  ;;
claude-code)
  npm view @anthropic-ai/claude-code version
  ;;
kiro)
  # kirodotdev/Kiro has zero GitHub releases (no releases API to poll) --
  # kiro.dev/changelog/cli/ is the only version signal, and it's a
  # client-rendered Next.js page, not a documented API: the version string
  # for each changelog entry appears in the server-rendered HTML immediately
  # followed by a category badge span reading "CLI" (entries for the Kiro IDE
  # itself carry an "IDE" badge instead, interleaved on the same page). This
  # is a scrape of undocumented page structure -- expect it to need repair if
  # kiro.dev's frontend markup changes; it is NOT a stable API contract.
  # Fetch first, THEN grep: piping curl directly into `grep -m1`/`head -1`
  # risks a SIGPIPE-induced failure under `set -o pipefail` once the
  # consumer stops reading early.
  page="$(curl -fsSL https://kiro.dev/changelog/cli/)"
  printf '%s' "$page" | grep -oP '[0-9]+\.[0-9]+\.[0-9]+(?=</span><span class="py-2 text-sm leading-\[150%\] text-muted-foreground">CLI</span>)' | head -1
  ;;
*)
  echo "detect-engine-version.sh: unknown engine '$engine'" >&2
  exit 1
  ;;
esac
