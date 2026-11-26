#!/usr/bin/env bash
# The one-writer debt only shrinks (owner ruling 2026-11-25: the daemon is the
# store's one writer). clippy's `disallowed-methods` (clippy.toml,
# crates/core/clippy.toml) refuses an angzarr kernel or kernel host started, and
# the store opened, outside the daemon; each remaining site is an
# `#[expect(clippy::disallowed_methods, reason = "P2 one-writer debt: <what
# replaces it>")]`. This counts those reasons in crates/**/*.rs and fails when
# there are more than scripts/one-writer-debt.max says, so a new site cannot be
# added silently; fewer is a note to lower the ceiling.
#
# Usage: one-writer-debt-check.sh [root]   (root defaults to the repository)
set -euo pipefail
top=${1:-$(git rev-parse --show-toplevel)}
cd "$top"
max_file=scripts/one-writer-debt.max
[ -f "$max_file" ] || { echo "FAIL lint-one-writer: no $max_file" >&2; exit 1; }
max=$(tr -d '[:space:]' < "$max_file")
[[ $max =~ ^[0-9]+$ ]] || { echo "FAIL lint-one-writer: $max_file is not a number: $max" >&2; exit 1; }
count=0
if [ -d crates ]; then
  count=$({ grep -rE --include='*.rs' 'reason = "P2 one-writer debt:' crates || true; } | wc -l | tr -d '[:space:]')
fi
if [ "$count" -gt "$max" ]; then
  echo "FAIL lint-one-writer: $count one-writer debt sites, the ceiling is $max ($max_file): a new site is a write that bypasses the daemon; send a command or fact to its coordinator instead" >&2
  exit 1
fi
if [ "$count" -lt "$max" ]; then
  echo "FAIL lint-one-writer: $count one-writer debt sites, below the ceiling $max: lower $max_file to $count" >&2
  exit 1
fi
echo "ok   lint-one-writer ($count debt sites, ceiling $max)"
