#!/usr/bin/env bash
# Stop hook: before a turn that CHANGED something is allowed to end, fire the
# `closeout` skill.
#
# It states NO part of the contract. The skill is the whole of it, so anything
# repeated here would be a second copy on the hot path — read every turn, and
# the expensive one to be wrong. Add nothing to the reason text; if an item
# needs explaining, explain it in the skill.
#
# Two guards decide whether this speaks at all, and both are load-bearing:
#
#   1. stop_hook_active. Claude Code sets it when the turn is already resuming
#      because a Stop hook blocked. Blocking again is how a Stop hook becomes
#      an infinite loop, so this exits first and unconditionally.
#   2. a turn that changed nothing. A turn that answered a question has
#      nothing to verify; nagging it burns tokens and trains the reader to
#      skim past the checklist on the turns where it matters.
#
# Exits 0 silently when either guard says stay quiet.

set -uo pipefail

input=$(cat)

if [ "$(jq -r '.stop_hook_active // false' <<<"$input" 2>/dev/null)" = "true" ]; then
  exit 0
fi

# UNATTENDED ESCAPE HATCH. Set by the `unattended` skill for a run with nobody
# watching, where every turn pays a full close-out and the loop is woken only by
# background jobs completing — so the per-turn tax comes straight out of the
# work the night was for. Silencing the hook does NOT lift the contract: the
# skill carries it and fires `closeout` itself.
#
# It is opt-IN and per-run: nothing sets it by default, and an interactive
# session never has it.
#
# THE MARKER IS WRITTEN BY THE AGENT ITSELF, not by a task-runner target: the
# unattended skill creates it at pre-flight and removes it at cleanup. An env
# var cannot serve here — shell state does not survive between an agent's tool
# calls, and this hook runs in Claude Code's environment rather than the
# agent's shell, so an `export` from a skill step would reach nothing.
#
# It lives under .ctxloom/state/, which ctxloom's own .ctxloom/.gitignore
# already ignores (/state). That is deliberate: .gitignore says the .ctxloom
# rules live in .ctxloom/.gitignore, "which ctxloom owns and rewrites
# wholesale", so a hand-added rule there would be clobbered. Choosing an
# already-ignored home needs no rule at all.
#
# THE MARKER GOES STALE, and that is the whole risk: a run that dies without
# removing it would silence every later INTERACTIVE session in this project,
# which is the failure this guard exists to prevent arriving by the back door.
# So it only counts while FRESH — a dead run stops mattering within the window
# instead of quietly disabling the checklist until somebody notices.
unattended_marker=".ctxloom/state/unattended"
if [ -f "$unattended_marker" ]; then
  # -mmin +720 selects files modified MORE than 12h ago; empty output means the
  # marker is fresh and the run that wrote it is plausibly still going.
  if [ -z "$(find "$unattended_marker" -mmin +720 2>/dev/null)" ]; then
    exit 0
  fi
  echo "ctxloom: ignoring stale unattended marker $unattended_marker (>12h old) — remove it if no run is active" >&2
fi

# Did THIS TURN change anything? -> `ctxloom hook turn-changed`, which reads
# the session transcript named by the payload and answers "changed" or
# "unchanged".
#
# The question used to be "is this checkout dirty", and that is the wrong
# proxy. It goes silent on precisely the sessions carrying the most
# close-out debt: a coordinator dispatches every edit into a separate git
# worktree, so its own tree stays clean-except-excluded for a whole night of
# work while it closes, cuts and files dozens of tasks. The turn is the unit
# of work, not the checkout the hook happens to run in — and measured on the
# turn, a subagent editing another worktree counts, while a conversational
# turn still does not.
#
# Only the exact word "unchanged" silences the contract. A missing binary, a
# crash, an unreadable transcript or any future word all leave it firing:
# silence is the failure this guard exists to prevent, and a spurious
# checklist is much the cheaper error.
if [ "$(printf '%s' "$input" | ctxloom hook turn-changed 2>/dev/null)" = "unchanged" ]; then
  exit 0
fi

read -r -d '' reason <<'EOF' || true
This turn changed files. Invoke the `closeout` skill and follow it.

Report what is now TRUE, and say "not done" where that is the truth.
EOF

jq -n --arg r "$reason" '{decision: "block", reason: $r}'
