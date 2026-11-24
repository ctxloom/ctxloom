#!/usr/bin/env bash
# Install rust-support's arch-lint at a pinned commit into <root>, unless it is
# already there. Usage: arch-lint-install.sh <root> <rev> <log>
#
# The install must never wait on a person or on the network for ever: it holds
# the gates' machine-wide staged-tree lock, so a hang stalls every commit. So
# git never prompts (GIT_TERMINAL_PROMPT=0, empty GIT_ASKPASS and SSH_ASKPASS),
# cargo runs under a deadline (ARCH_LINT_TIMEOUT seconds, default 900), and its
# output goes to <log>, not a pipe a killed cargo could leave a reader on.
# ARCH_LINT_REPO overrides the repository URL (the self-test's seam).
set -euo pipefail
root=$1 rev=$2 log=$3
repo=${ARCH_LINT_REPO:-https://github.com/benjaminabbitt/rust-support}
limit=${ARCH_LINT_TIMEOUT:-900}

[ -x "$root/bin/arch-lint" ] && exit 0
mkdir -p "$(dirname "$log")"
rc=0
GIT_TERMINAL_PROMPT=0 GIT_ASKPASS= SSH_ASKPASS= \
  timeout --kill-after=10 "$limit" \
  cargo install --quiet --git "$repo" --rev "$rev" arch-lint --locked --root "$root" \
  >"$log" 2>&1 </dev/null || rc=$?
[ "$rc" = 0 ] && exit 0
case $rc in
  124|137) why="no result within ${limit}s" ;;
  *) why="cargo install exited $rc" ;;
esac
echo "FAIL lint-arch: arch-lint could not be installed ($why; log: $log)" >&2
tail -n 10 "$log" >&2 || true
exit 1
