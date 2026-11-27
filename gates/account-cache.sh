#!/usr/bin/env bash
# account-cache.sh: print the user-wide gates cache directory,
# `${XDG_CACHE_HOME:-<the account's home>/.cache}/ctxloom/gates`.
#
# The ONE resolution of where the gates keep user-wide state (the staged
# target and its lock, the machine-wide test lock). The account's home comes
# from the passwd database and never from `$HOME`: an agent session runs with
# HOME set to a per-session home (ruling 112), so a path keyed on `$HOME` is
# a different file in every session and a "machine-wide" lock is not (found
# 2026-11-27: workspace-tests.lock in ten session homes). `$HOME` is only the
# last resort when the passwd database and the shell both fail.
# Used by gates/tests-lock.sh, `_scratch-tree` in gates/gates.just and
# scripts/land-sweep.sh. Self-test: scripts/tests-lock.selftest.sh.
set -euo pipefail
cache=${XDG_CACHE_HOME:-}
if [ -z "$cache" ]; then
    account=$(getent passwd "$(id -u)" 2> /dev/null | cut -d: -f6 || true)
    [ -n "$account" ] || account=$(eval echo "~$(id -un)")
    cache=${account:-$HOME}/.cache
fi
printf '%s\n' "$cache/ctxloom/gates"
