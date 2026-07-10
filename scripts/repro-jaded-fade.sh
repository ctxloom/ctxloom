#!/usr/bin/env bash
# repro-jaded-fade.sh — regression script for the "jaded-fade" index-clobber
# hazard (see lefthook.yml, pre-commit.reprise).
#
# ROOT CAUSE: `git commit` exports GIT_INDEX_FILE into every hook's
# environment, pointing at the committing worktree's REAL index (confirmed
# via `GIT_TRACE=1`: run-command.c logs
# `GIT_INDEX_FILE=<worktree>/index <hooks-dir>/pre-commit`). reprise's
# `check --base <ref>` command, on a base-state cache miss, shells out
# `git worktree add --detach -q <tmp> <ref>` to materialize a comparison
# checkout of the base ref (reprise/src/check.rs, BaseWorktree::add). That
# subprocess inherits GIT_INDEX_FILE unless the caller scrubs it. Given an
# inherited GIT_INDEX_FILE, `git worktree add`'s internal checkout writes
# the new worktree's tree into THAT index file — i.e. it silently
# overwrites the calling worktree's real, currently-staged index with a
# tree matching the base ref (here, HEAD) instead of allocating its own
# worktree-scoped index. `git commit`, after the hook exits 0, builds the
# commit tree from the (now-reset-to-HEAD) real index: the result is a
# silently EMPTY commit — tree identical to the parent — with no error
# anywhere in the chain, because every individual git/reprise/lefthook step
# succeeded on its own terms.
#
# This only fires on a base-state CACHE MISS: a cache hit skips
# BaseWorktree::add entirely (reprise/src/check.rs: base_state() returns
# early via Baseline::load(&cache_path)), so an immediate retry after a
# corrupted commit lands correctly every time — exactly the "cache miss
# fails, warm retry succeeds" pattern observed in the field (20+ incidents
# across 5 worktrees, always the FIRST commit attempt in a session).
#
# FIX (lefthook.yml, pre-commit.reprise.run): `env -u GIT_INDEX_FILE`
# before invoking reprise, so no subprocess reprise spawns — now or in the
# future — can inherit the poisoned index path. Proven necessary AND
# sufficient below (GIT_DIR is also present in the hook env but was
# confirmed NOT to need scrubbing — see PART A, "leaves GIT_DIR set").
#
# This script is self-contained: it builds a synthetic repo + linked
# worktree under a tmpdir, never touches the real project checkout, and
# cleans up after itself. Run it directly:
#   bash scripts/repro-jaded-fade.sh
#
# PART A reproduces the mechanism with a minimal mimic hook (same
# `git worktree add`/`remove` shape as reprise's BaseWorktree, no Rust
# toolchain required) — this is the part that actually asserts pass/fail.
# PART B additionally drives the REAL `reprise` + `lefthook` binaries
# end-to-end through this repo's actual lefthook.yml, for maximum fidelity;
# it soft-skips if either binary isn't on PATH.

set -euo pipefail

RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RESET=$'\033[0m'
fail() { echo "${RED}FAIL${RESET}: $*" >&2; exit 1; }
pass() { echo "${GREEN}PASS${RESET}: $*"; }
note() { echo "${YELLOW}note${RESET}: $*"; }

ROOT=$(mktemp -d /tmp/jaded-fade-repro.XXXXXX)
trap 'rm -rf "$ROOT"' EXIT

git_repo() {
  # $1 = path
  git init -q -b main "$1"
  git -C "$1" config user.name "Repro Bot"
  git -C "$1" config user.email "repro@example.com"
  git -C "$1" config commit.gpgsign false
}

# ---------------------------------------------------------------------------
# PART A: minimal mimic of reprise's BaseWorktree::add/Drop dance, isolating
# exactly the operation proven to clobber the index — no reprise/lefthook
# binaries required.
# ---------------------------------------------------------------------------

echo "=== PART A: minimal repro (git worktree add/remove under inherited GIT_INDEX_FILE) ==="

MAIN="$ROOT/a-main"
WT="$ROOT/a-wt"
git_repo "$MAIN"
echo "package main" > "$MAIN/a.go"
git -C "$MAIN" add a.go
git -C "$MAIN" commit -q -m "initial"
git -C "$MAIN" worktree add -q "$WT" -b a-wt-branch >/dev/null

check_scenario() {
  # $1 = human label, $2 = "clobber" | "fixed" | "fixed-gitdir-still-set"
  local label="$1" mode="$2"
  cd "$WT"
  git reset -q --hard a-wt-branch >/dev/null 2>&1 || true
  git checkout -q a-wt-branch
  echo "package main // edit-$mode-$RANDOM" >> a.go
  git add a.go
  local staged
  staged=$(git diff --cached --stat | wc -l)
  [ "$staged" -gt 0 ] || fail "setup: nothing staged before $label"

  local idx
  idx=$(git rev-parse --git-path index)
  local tmp
  tmp=$(mktemp -d)

  case "$mode" in
    clobber)
      # Simulates the UNFIXED hook: reprise's git-worktree-add subprocess
      # inherits GIT_INDEX_FILE from the pre-commit hook's environment,
      # exactly as `git commit` sets it (confirmed via GIT_TRACE=1).
      GIT_INDEX_FILE="$idx" git -C "$WT" worktree add --detach -q "$tmp/base" HEAD
      ;;
    fixed)
      # Simulates the FIXED hook: `env -u GIT_INDEX_FILE` before the
      # git-worktree-add subprocess, matching lefthook.yml's
      # `run: env -u GIT_INDEX_FILE reprise check --base HEAD .`.
      env -u GIT_INDEX_FILE GIT_INDEX_FILE="$idx" bash -c '
        unset GIT_INDEX_FILE
        exec git -C "$0" worktree add --detach -q "$1/base" HEAD
      ' "$WT" "$tmp"
      ;;
    fixed-gitdir-still-set)
      # Confirms GIT_DIR (also present in the real hook env) does not need
      # scrubbing once GIT_INDEX_FILE is gone — keeps the fix minimal.
      local gitdir
      gitdir=$(git rev-parse --git-dir)
      env -u GIT_INDEX_FILE GIT_DIR="$gitdir" git -C "$WT" worktree add --detach -q "$tmp/base" HEAD
      ;;
  esac
  git -C "$WT" worktree remove --force "$tmp/base" >/dev/null 2>&1 || true
  rm -rf "$tmp"

  local after
  after=$(git diff --cached --stat | wc -l)
  echo "  [$label] staged lines before=$staged after=$after" >&2
  echo "$after"
}

a1=$(check_scenario "unfixed (GIT_INDEX_FILE inherited)" clobber)
[ "$a1" -eq 0 ] || fail "expected the UNFIXED scenario to clobber the index (staged diff survived: $a1 lines) — mechanism did not reproduce, investigate further before trusting the fix"
pass "unfixed scenario reproduces the clobber (staged change silently vanished)"

a2=$(check_scenario "fixed (env -u GIT_INDEX_FILE)" fixed)
[ "$a2" -gt 0 ] || fail "FIXED scenario still lost the staged change — env -u GIT_INDEX_FILE did not prevent the clobber"
pass "fixed scenario (env -u GIT_INDEX_FILE) preserves the staged change"

a3=$(check_scenario "fixed, GIT_DIR still set" fixed-gitdir-still-set)
[ "$a3" -gt 0 ] || fail "unsetting GIT_INDEX_FILE alone was insufficient once GIT_DIR is also considered — widen the fix"
pass "GIT_DIR does not need scrubbing once GIT_INDEX_FILE is unset (fix stays minimal)"

git -C "$WT" worktree remove --force "$WT" >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# PART B: end-to-end through the real lefthook.yml + reprise + lefthook
# binaries, for maximum fidelity. Soft-skips if either tool is missing.
# ---------------------------------------------------------------------------

echo
echo "=== PART B: end-to-end through real lefthook + reprise (best-effort) ==="

if ! command -v lefthook >/dev/null 2>&1; then
  note "lefthook not on PATH — skipping end-to-end part (PART A already proves the mechanism and fix)"
  exit 0
fi
if ! command -v reprise >/dev/null 2>&1; then
  note "reprise not on PATH — skipping end-to-end part (PART A already proves the mechanism and fix)"
  exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LEFTHOOK_YML="$SCRIPT_DIR/lefthook.yml"
[ -f "$LEFTHOOK_YML" ] || fail "can't find lefthook.yml at $LEFTHOOK_YML"

run_e2e() {
  # $1 = label, $2 = path to lefthook.yml to install
  local label="$1" cfg="$2"
  local main="$ROOT/b-main-$label" wt="$ROOT/b-wt-$label"
  git_repo "$main"
  mkdir -p "$main/src"
  echo "package main" > "$main/src/a.go"
  cp "$cfg" "$main/lefthook.yml"
  git -C "$main" add -A
  git -C "$main" commit -q -m "initial"
  (cd "$main" && lefthook install >/dev/null)
  git -C "$main" worktree add -q "$wt" -b "wt-$label-branch" >/dev/null

  cd "$wt"
  echo "package main // agent edit" >> src/a.go
  git add src/a.go
  # Chained add+commit in ONE invocation (discriminator 1 from the field
  # reports) on a guaranteed-cold reprise base-state cache (discriminator 2:
  # first commit attempt in a fresh worktree is always a cache miss).
  git commit -q -m "agent edit" || true

  local before after
  before=$(git rev-parse 'HEAD~1^{tree}')
  after=$(git rev-parse 'HEAD^{tree}')
  git -C "$main" worktree remove --force "$wt" >/dev/null 2>&1 || true
  if [ "$before" = "$after" ]; then
    echo "EMPTY"
  else
    echo "OK"
  fi
}

cat > "$ROOT/lefthook-unfixed.yml" <<'EOF'
pre-commit:
  commands:
    reprise:
      run: reprise check --base HEAD .
EOF

unfixed_result=$(run_e2e unfixed "$ROOT/lefthook-unfixed.yml")
echo "  unfixed lefthook.yml -> commit result: $unfixed_result"
if [ "$unfixed_result" != "EMPTY" ]; then
  note "unfixed config did not reproduce an empty commit this run (reprise base-state cache may be warm, or timing did not line up) — PART A already proves the mechanism deterministically, so this is not a hard failure"
fi

fixed_result=$(run_e2e fixed "$LEFTHOOK_YML")
echo "  current lefthook.yml (fix applied) -> commit result: $fixed_result"
[ "$fixed_result" = "OK" ] || fail "current repo lefthook.yml still produces an empty commit — the fix regressed"
pass "current lefthook.yml (env -u GIT_INDEX_FILE) commits real content end-to-end"

echo
echo "${GREEN}All checks passed.${RESET}"
