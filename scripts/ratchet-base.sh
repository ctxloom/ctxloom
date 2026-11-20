#!/usr/bin/env bash
# The base's scenario list for the ratchet (owner ruling 165): `just
# ratchet-base <base>`. The feature runner (`every_feature_runs`, under
# GHERKIN_RATCHET) is run on the base commit, checked out alone in a
# throwaway worktree beside the checkout (.<checkout>.ratchet-base, its own
# target dir target/ratchet-base), and its list is cached by the base's sha
# at target/ratchet/<sha>.list; a cached list is used as it is.
#
#   ok   ratchet-base (1a2b3c4: 136 red of 512) 301.0s
#
# A base from before the runner could list its scenarios gets the commit
# that taught it (the first in HEAD's history adding GHERKIN_RATCHET to
# gherkin_features.test.rs) applied on it, its runner files only, and says
# so; one it does not apply to fails. Only that test runs, so nothing but the
# list is judged on the base.
#
# The worktree is removed once the list is cached; an interrupted run leaves
# it, at the same sha, so the next run resumes the build where it stopped
# (cargo fingerprints by mtime). Exit 0 with the list cached, 1 otherwise.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
scrub=(env)
while IFS= read -r var; do scrub+=(-u "$var"); done < <(git rev-parse --local-env-vars)
g() { "${scrub[@]}" git "$@"; }

top=$(g rev-parse --show-toplevel)
[ -n "${1:-}" ] || { echo "FAIL ratchet-base: usage: ratchet-base <base>" >&2; exit 1; }
base_sha=$(g -C "$top" rev-parse --verify --quiet "$1^{commit}") || { echo "FAIL ratchet-base: $1 is not a commit" >&2; exit 1; }
short=${base_sha:0:7}
cache_dir=$top/target/ratchet
cache=$cache_dir/$base_sha.list
mkdir -p "$cache_dir" "$top/target/logs"
log=$top/target/logs/ratchet-base.log
summary() { printf 'ok   ratchet-base (%s: %d red of %d)%s\n' "$short" "$(grep -c '^red' "$cache" || true)" "$(grep -c '' "$cache")" "$1"; }
if [ -s "$cache" ]; then
    summary " cached"
    exit 0
fi

exec 7> "$top/target/.ratchet-base.lock"
flock 7
runner=crates/root/bin/ctxloom/src/gherkin_features.test.rs
wt=$(dirname "$top")/.$(basename "$top").ratchet-base
stamp=$cache_dir/worktree.stamp
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT TERM HUP

emitter=
if ! g -C "$top" grep -q GHERKIN_RATCHET "$base_sha" -- "$runner"; then
    emitter=$(g -C "$top" log --reverse --format=%H -S GHERKIN_RATCHET HEAD -- "$runner" | head -n 1)
    if [ -z "$emitter" ]; then
        echo "FAIL ratchet-base: $short cannot list its scenarios, and HEAD has no commit that teaches it (GHERKIN_RATCHET)" >&2
        exit 1
    fi
fi
want="$base_sha ${emitter:-none}"
remove_tree() {
    # The base's tree and its own record only, never a repository-wide prune
    # (in a container that mounts only this checkout it deletes every other
    # worktree's record): gates/drop-worktree.sh.
    "${scrub[@]}" "$here/../gates/drop-worktree.sh" "$top" "$wt"
    rm -f "$stamp"
}
if [ ! -d "$wt" ] || [ "$(cat "$stamp" 2>/dev/null)" != "$want" ] \
    || [ "$(g -C "$wt" rev-parse HEAD 2>/dev/null)" != "$base_sha" ]; then
    remove_tree
    g -C "$top" worktree add --quiet --detach --no-checkout "$wt" "$base_sha"
    g -C "$wt" read-tree -u --reset HEAD
    if [ -n "$emitter" ]; then
        g -C "$top" diff --binary --no-color --no-ext-diff "$emitter^" "$emitter" \
            -- 'crates/root/bin/ctxloom/src/gherkin*.test.rs' > "$work/emitter.patch"
        if ! g -C "$wt" apply --whitespace=nowarn "$work/emitter.patch" 2> "$work/apply.err"; then
            echo "FAIL ratchet-base: $short predates the scenario list, and its commit ${emitter:0:7} does not apply to it; merge main into the base first" >&2
            head -n 10 "$work/apply.err" >&2
            remove_tree
            exit 1
        fi
    fi
    echo "$want" > "$stamp"
fi
[ -z "$emitter" ] || echo "note ratchet-base: $short predates the scenario list; ${emitter:0:7}'s runner change was applied on it"

start=$(date +%s)
rc=0
(cd "$wt" && "${scrub[@]}" "CARGO_TARGET_DIR=$top/target/ratchet-base" "GHERKIN_RATCHET=$work/list" \
    cargo nextest run --manifest-path "$wt/Cargo.toml" --package ctxloom --no-fail-fast --no-tests=fail \
    --status-level fail --final-status-level fail --success-output never --failure-output final \
    --hide-progress-bar --cargo-quiet -E 'test(=tests::gherkin_features::every_feature_runs)') \
    > "$log" 2>&1 7>&- || rc=$?
secs=$(($(date +%s) - start))
if [ "$rc" -ne 0 ] || [ ! -s "$work/list" ]; then
    echo "FAIL ratchet-base: the feature runner did not list $short's scenarios (nextest exit $rc) (full log: target/logs/ratchet-base.log)" >&2
    grep -E '^[^ ]+:[0-9]+:[0-9]+: error|^error(\[[A-Z0-9]+\])?: |^ +FAIL ' "$log" | head -n 20 >&2 || true
    exit 1
fi
mv "$work/list" "$cache"
remove_tree
summary " ${secs}.0s"
