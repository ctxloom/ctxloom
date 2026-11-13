#!/usr/bin/env bash
# ctxloom's `red_judge` (gates/gates.just's `test-commit`): whether the red
# tests of a commit off the default branch are ones it may carry. A red commit
# may add red tests and change the expectations of the tests it touches; it
# may not turn red a scenario of a feature file it does not touch. That is
# ruling 165's ratchet (a slice may add red, never break green) judged at each
# commit against HEAD, with the commit's own feature files exempt, so a RED
# commit can still change a scenario's expectation. Commit fd50e0b77 is why:
# run.feature went red, untouched, and the gate committed it as RED.
#
# Only the feature runner is judged: when the test-staged log does not name
# `every_feature_runs` failing, every red test is allowed, as before (a red
# unit test is not compared with HEAD). Otherwise the scenario lists are made,
# each by the feature runner under GHERKIN_RATCHET: the staged tree's
# (`just _staged-tree _feature-list <file>`) and HEAD's (`just ratchet-base
# HEAD`, cached by sha). A scenario red here and not red at HEAD whose
# feature file (features/<path>) is not in the staged change is refused:
#
#   FAIL red-commit: 1 scenario turned red in a feature file this commit does not touch (...)
#     cli/run.feature :: Her settings are hers
#
# A list that cannot be made refuses (nothing can be judged). At most
# RATCHET_LIST_MAX (default 30) are named. Exit 0 allowed, 1 refused.
#
# Seams (the self-test's): RED_JUDGE_LOG (the test-staged log),
# RED_JUDGE_STAGED_LIST and RED_JUDGE_BASE_LIST (the two lists, made by
# nobody when given).
set -euo pipefail
top=$(git rev-parse --show-toplevel)
features=crates/root/bin/ctxloom/features
cap=${RATCHET_LIST_MAX:-30}
log=${RED_JUDGE_LOG:-$top/target/logs/test-staged.log}
refuse() { echo "FAIL red-commit: $*" >&2; exit 1; }

if ! grep -Eq '^ +FAIL \[ *[0-9.]+s\] ctxloom::bin/ctxloom tests::gherkin_features::every_feature_runs$' "$log" 2>/dev/null; then
    exit 0
fi
dir=$top/target/logs/red-commit
mkdir -p "$dir"
staged_list=${RED_JUDGE_STAGED_LIST:-$dir/staged.list}
if [ -z "${RED_JUDGE_STAGED_LIST:-}" ]; then
    rm -f "$staged_list"
    just --justfile "$top/justfile" _staged-tree _feature-list "$staged_list" > "$dir/staged.log" 2>&1 || true
fi
if [ -z "${RED_JUDGE_BASE_LIST:-}" ]; then
    head_sha=$(git -C "$top" rev-parse HEAD)
    just --justfile "$top/justfile" ratchet-base "$head_sha" > "$dir/base.log" 2>&1 \
        || refuse "HEAD's scenario list could not be made, so the red feature runner cannot be judged (log: target/logs/red-commit/base.log)"
    base_list=$top/target/ratchet/$head_sha.list
else
    base_list=$RED_JUDGE_BASE_LIST
fi
[ -s "$staged_list" ] || refuse "the staged tree's scenario list could not be made, so the red feature runner cannot be judged (log: target/logs/red-commit/staged.log)"
[ -s "$base_list" ] || refuse "HEAD's scenario list is empty or missing ($base_list), so the red feature runner cannot be judged"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sed -n 's/^red\t//p' "$base_list" | LC_ALL=C sort -u > "$work/base.red"
sed -n 's/^red\t//p' "$staged_list" | LC_ALL=C sort -u > "$work/staged.red"
LC_ALL=C comm -13 "$work/base.red" "$work/staged.red" > "$work/new"
git -C "$top" diff --cached --name-only --no-renames HEAD > "$work/paths"
: > "$work/refused"
while IFS= read -r id; do
    file=${id%% :: *}
    grep -qxF "$features/$file" "$work/paths" || printf '%s\n' "$id" >> "$work/refused"
done < "$work/new"
n=$(grep -c '' "$work/refused" || true)
[ "$n" -eq 0 ] && exit 0
noun=scenarios
[ "$n" -eq 1 ] && noun=scenario
echo "FAIL red-commit: $n $noun turned red in a feature file this commit does not touch" \
    "(red it did not add: fix it, or commit the change that makes it red with its feature's change; list: $staged_list)" >&2
head -n "$cap" "$work/refused" | sed 's/^/  /' >&2
[ "$n" -le "$cap" ] || echo "  +$((n - cap)) more" >&2
exit 1
