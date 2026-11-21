#!/usr/bin/env bash
# The pre-commit test gate's narrowing (`staged_tests_filter`, the
# justfile's; gates/gates.just `test-staged`): given the staged paths, one
# per line on stdin, print the nextest filterset its run is held to. Every
# test but the feature runner's tests of one feature file each
# (`tests::gherkin_features::features::*`, gherkin_features.test.rs), and of
# those only the ones whose feature file the commit stages: a commit whose
# change merely reaches the binary runs none of its features, and the whole
# suite is the landing gate's (`land-ratchet`, `closeout-ratchet`).
#
# A feature's test is named by its path under the binary's `features/`:
# its directories are modules, its file name (without `.feature`) the test,
# so `crates/root/bin/ctxloom/features/cli/agent.feature` is
# `tests::gherkin_features::features::cli::agent` (`test_of_feature`).
# A staged feature that is deleted names a test nobody has, which selects
# nothing.
set -euo pipefail
features=crates/root/bin/ctxloom/features/
prefix=tests::gherkin_features::features::
staged=()
while IFS= read -r path; do
    case "$path" in
        "$features"*.feature)
            rel=${path#"$features"}
            rel=${rel%.feature}
            staged+=("test(=$prefix${rel//\//::})")
            ;;
    esac
done
set="not (package(ctxloom) & test(/^${prefix}/))"
if [ "${#staged[@]}" -gt 0 ]; then
    joined=$(printf ' | %s' "${staged[@]}")
    set="$set | (package(ctxloom) & (${joined# | }))"
fi
printf '%s\n' "$set"
