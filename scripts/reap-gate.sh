#!/usr/bin/env bash
# reap-gate.sh — may this agent worktree be removed?
#
# A delivered report is NOT proof an agent has terminated. On 2026-07-21 a
# coordinator read a first report as final, cherry-picked, and removed a
# worktree while its agent was still running; the working directory vanished
# mid-session. Nothing was lost only because the work was already committed.
# Agents can also send MULTIPLE reports — a premature one, then a corrected
# final one.
#
# So the gate is a script, not a rule to remember. This project has measured
# that rules-to-remember fail against its own agents.
#
# Usage:  scripts/reap-gate.sh <worktree-path> [integration-branch]
# Exit:   0 = safe to remove.  1 = DO NOT REMOVE (reason printed).
set -uo pipefail

WT="${1:?usage: reap-gate.sh <worktree-path> [integration-branch]}"
TARGET="${2:-release/0.7}"
FAIL=0

note() { printf '  %s\n' "$*"; }
bad()  { printf 'BLOCKED: %s\n' "$*"; FAIL=1; }

[ -d "$WT" ] || { echo "no such worktree: $WT"; exit 1; }
WT_ABS="$(cd "$WT" && pwd -P)"
echo "reap-gate: $WT_ABS (integration: $TARGET)"

# --- 1. EXITED ------------------------------------------------------------
# pid-exit alone misses the agent's ORPHANED CHILDREN, which are exactly what
# leaks here. Scan for any process whose cwd or open files live under the tree.
echo "[1/3] no live process inside the tree"
LIVE=""
for p in /proc/[0-9]*; do
    pid="${p#/proc/}"
    cwd="$(readlink "$p/cwd" 2>/dev/null)" || continue
    case "$cwd" in "$WT_ABS"|"$WT_ABS"/*) LIVE="$LIVE $pid";; esac
done
if [ -n "${LIVE// /}" ]; then
    bad "processes still live inside the worktree:$LIVE"
    for pid in $LIVE; do note "pid $pid: $(tr '\0' ' ' < /proc/$pid/cmdline 2>/dev/null | cut -c1-100)"; done
else
    note "clear"
fi

# --- 2. CLEAN -------------------------------------------------------------
# Untracked counts. Uncommitted work in a removed worktree is gone forever,
# and WIP is sacred — preserve, never reap.
echo "[2/3] working tree clean (untracked included)"
DIRTY="$(git -C "$WT_ABS" status --porcelain 2>/dev/null)"
if [ -n "$DIRTY" ]; then
    bad "uncommitted or untracked work present — PRESERVE, do not remove"
    printf '%s\n' "$DIRTY" | head -20 | sed 's/^/    /'
else
    note "clean"
fi

# --- 3. LANDED ------------------------------------------------------------
# Verify against the ACTUAL integration branch, which is often not main.
echo "[3/3] commits reachable from $TARGET"
BR="$(git -C "$WT_ABS" rev-parse --abbrev-ref HEAD 2>/dev/null)"
if [ "$BR" = "HEAD" ]; then
    note "detached HEAD — skipping landed check (ephemeral tree)"
elif ! git -C "$WT_ABS" rev-parse --verify -q "$TARGET" >/dev/null; then
    bad "integration branch '$TARGET' does not exist — cannot verify landing"
else
    UNMERGED="$(git -C "$WT_ABS" log --oneline "$TARGET..$BR" 2>/dev/null)"
    if [ -n "$UNMERGED" ]; then
        bad "commits on '$BR' not reachable from '$TARGET':"
        printf '%s\n' "$UNMERGED" | head -20 | sed 's/^/    /'
    else
        note "all commits landed"
    fi
fi

echo
if [ "$FAIL" -eq 0 ]; then
    echo "SAFE TO REMOVE: $WT_ABS"
else
    echo "DO NOT REMOVE: $WT_ABS  (triage: merged+clean -> remove; dirty/unmerged -> report to a human)"
fi
exit "$FAIL"
