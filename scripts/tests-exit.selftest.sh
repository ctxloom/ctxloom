#!/usr/bin/env bash
# `_tests`' exit codes, and `test-commit`'s reading of them (owner ruling
# 144), checked on a fixture workspace. `_tests` never passes nextest's own
# code through: nextest gives 102 to "cargo metadata failed" and 105 to a
# failed setup script
# (https://docs.rs/nextest-metadata/latest/nextest_metadata/enum.NextestExitCode.html),
# which a pass-through once let `test-commit` commit as red tests.
#   0 passed; 100 a test failed; 101 not built; 120 a doc test failed;
#   121 nextest failed some other way (`nextest exited <code>` in the log).
# 100 and 120 are red (committed off the default branch only); 101 and 121
# are refused on every branch. Its memory cap: opted out and said, and
# with no user bus a 121 before anything is built. The gates are
# gates/gates.just (or $GATES_JUST).
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
here=$(cd "$(dirname "$0")" && pwd)
gates=${GATES_JUST:-$here/../gates/gates.just}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL tests-exit.selftest: $*" >&2; exit 1; }

fx=$tmp/fx
mkdir -p "$fx/gates" "$fx/crates/a/src" "$tmp/bin"
cp "$gates" "$fx/gates/gates.just"
cp "$(dirname "$gates")/drop-worktree.sh" "$fx/gates/drop-worktree.sh"
cp "$(dirname "$gates")/memory-cap.sh" "$fx/gates/memory-cap.sh"
cp "$(dirname "$gates")/tests-lock.sh" "$fx/gates/tests-lock.sh"
cp "$(dirname "$gates")/account-cache.sh" "$fx/gates/account-cache.sh"
cp "$(dirname "$gates")/red-sanity.sh" "$fx/gates/red-sanity.sh"
# The fixture's workspace runs take a test lock of their own, never the
# machine's (gates/tests-lock.sh), and are inside no other run's.
export TESTS_LOCK_FILE=$tmp/workspace-tests.lock
unset GATES_TESTS_LOCK_HELD
cat > "$fx/Cargo.toml" <<'T'
[workspace]
members = ["crates/a"]
resolver = "2"
T
cat > "$fx/crates/a/Cargo.toml" <<'T'
[package]
name = "a"
version = "0.1.0"
edition = "2021"
T
# `test-staged` stands in for the real one, which needs a staged tree: it
# exits as FAKE_STAGED_RC says, so `test-commit`'s reading of each code is
# what is checked, and says the scope it was asked for: none (the changed
# crates' own tests) off the default branch, `dependents` on it.
cat > "$fx/justfile" <<'J'
set allow-duplicate-recipes := true
TOP := `git rev-parse --show-toplevel`
import 'gates/gates.just'
staged_tree := "../.{name}.staged"
feature_rules := ""
features_prepare := ""
mutation_thresholds := "crates=90"
mutants_memory_max := "16G"
mutants_swap_max := "2G"
tests_memory_max := "none"
tests_swap_max := "0"
workspace_wide := "Cargo.toml"
red_judge := "_judge"
staged_tests_filter := ""
[no-exit-message]
test-staged scope="":
    @echo "test-staged asked: [{{scope}}]"; exit "${FAKE_STAGED_RC}"
[no-exit-message]
_judge rc:
    @echo "judge asked: {{rc}}"; exit "${FAKE_JUDGE_RC:-0}"
J
# `cargo`, as `_tests` finds it on PATH: the real one, except that `cargo
# nextest` exits FAKE_NEXTEST_RC when it is set (a code no fixture can make
# nextest give, such as a metadata failure).
real_cargo=$(command -v cargo)
cat > "$tmp/bin/cargo" <<S
#!/usr/bin/env bash
if [ "\${1:-}" = nextest ] && [ -n "\${FAKE_NEXTEST_RC:-}" ]; then
    echo "fake nextest: exit \$FAKE_NEXTEST_RC"
    exit "\$FAKE_NEXTEST_RC"
fi
exec "$real_cargo" "\$@"
S
chmod +x "$tmp/bin/cargo"
export PATH=$tmp/bin:$PATH CARGO_TARGET_DIR=$tmp/target CARGO_TERM_COLOR=never
git -C "$fx" init -q -b main
git -C "$fx" -c user.email=t@e -c user.name=t commit -q --allow-empty -m init

lib() { printf '%s\n' "$1" > "$fx/crates/a/src/lib.rs"; }
doc_ok='/// ```
/// assert_eq!(a::one(), 1);
/// ```
pub fn one() -> u32 { 1 }'
doc_bad='/// ```
/// assert_eq!(a::one(), 2);
/// ```
pub fn one() -> u32 { 1 }'
unit() { printf '#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() { assert_eq!(super::one(), %s); }\n}\n' "$1"; }

tests_rc() { # name expected [env...]
    local name=$1 want=$2 rc=0
    shift 2
    (cd "$fx" && env "$@" just _tests --workspace > "$tmp/out" 2>&1) || rc=$?
    [ "$rc" -eq "$want" ] || fail "_tests, $name: exit $rc, want $want: $(tail -n 20 "$tmp/out")"
}
lib "$doc_ok
$(unit 1)"; tests_rc "everything passes" 0
lib "$doc_ok
$(unit 2)"; tests_rc "a test fails" 100
lib "$doc_bad
$(unit 1)"; tests_rc "a doc test fails" 120
lib "pub fn one() -> u32 { \"s\" }"; tests_rc "the tests do not build" 101
lib "$doc_ok
$(unit 1)"
for code in 102 105 104 4; do
    tests_rc "nextest exits $code" 121 FAKE_NEXTEST_RC="$code"
    grep -q "^nextest exited $code: not a test failure" "$tmp/out" || fail "_tests, nextest $code: not named: $(cat "$tmp/out")"
done
# `_tests-checked` (what `test-staged` runs) is `_tests` plus
# gates/red-sanity.sh on a red run (scripts/red-commit-sanity.selftest.sh has
# the rule's cases): a test that fails is still 100; one that fails and takes
# the worktree's .git with it is 122, the environment's failure, said in the
# extract, and green stays 0.
checked_rc() { # name expected
    local rc=0
    (cd "$fx" && just _tests-checked --workspace > "$tmp/out" 2>&1) || rc=$?
    [ "$rc" -eq "$2" ] || fail "_tests-checked, $1: exit $rc, want $2: $(tail -n 20 "$tmp/out")"
}
lib "$doc_ok
$(unit 1)"; checked_rc "everything passes" 0
lib "$doc_ok
$(unit 2)"; checked_rc "a test fails" 100
lib "$doc_ok
#[cfg(test)]
mod tests {
    #[test]
    fn t() { std::fs::remove_dir_all(\"../../.git\").unwrap(); panic!(\"gone\"); }
}"; checked_rc "a test fails and the worktree is gone" 122
grep -q '^red-sanity: the staged worktree is gone' "$tmp/out" || fail "_tests-checked, worktree gone: not said: $(cat "$tmp/out")"
git -C "$fx" init -q -b main
git -C "$fx" -c user.email=t@e -c user.name=t commit -q --allow-empty -m init
said=$(cd "$fx" && just _extract-tests "$tmp/out" 122)
grep -q "^not red tests, the environment's failure: the staged worktree is gone" <<< "$said" || fail "extract 122: $said"
lib "$doc_ok
$(unit 1)"
# The memory cap (gates/memory-cap.sh, `tests_memory_max`): the fixture
# opts out, and the run says so; with a cap and no user bus to reach, the
# run FAILS before anything is built, 121, the cap's FAIL line and its fix
# naming TESTS_MEMORY_MAX; where a user scope can be had, a capped run
# passes and ends with the scope's peak.
tests_rc "uncapped" 0
grep -q "^tests: memory cap: none (TESTS_MEMORY_MAX=none): this run is UNCAPPED" "$tmp/out" \
    || fail "_tests, uncapped: the opt-out is not said: $(cat "$tmp/out")"
if "$fx/gates/memory-cap.sh" probe 1G 0 --check > /dev/null 2>&1; then
    rc=0
    (cd "$fx" && just --set tests_memory_max 2G --set tests_swap_max 0 _tests --workspace > "$tmp/out" 2>&1) || rc=$?
    [ "$rc" -eq 0 ] || fail "_tests, capped: exit $rc, want 0: $(tail -n 20 "$tmp/out")"
    grep -q "^tests: memory peak .* (cap MemoryMax=2G MemorySwapMax=0)" "$tmp/out" \
        || fail "_tests, capped: no peak line: $(cat "$tmp/out")"
else
    echo "SKIP tests-exit.selftest (a capped run): no systemd user scope here"
fi
mkdir -p "$tmp/run-user/$(id -u)"
rm -rf "$tmp/target"
rc=0
(cd "$fx" && env -u XDG_RUNTIME_DIR -u DBUS_SESSION_BUS_ADDRESS "MEMORY_CAP_RUN_USER=$tmp/run-user" \
    just --set tests_memory_max 1G _tests --workspace > "$tmp/out" 2>&1) || rc=$?
[ "$rc" -eq 121 ] || fail "_tests, no bus: exit $rc, want 121: $(cat "$tmp/out")"
grep -q "^FAIL tests: no memory cap" "$tmp/out" || fail "_tests, no bus: not said: $(cat "$tmp/out")"
grep -q "^  fix: .*TESTS_MEMORY_MAX=none" "$tmp/out" || fail "_tests, no bus: no fix naming the opt-out: $(cat "$tmp/out")"
[ ! -e "$tmp/target" ] || fail "_tests, no bus: something was built uncapped"

# The extract says which, so a FAIL line never calls a runner failure red.
printf 'nextest exited 102: not a test failure\n' > "$tmp/log"
said=$(cd "$fx" && just _extract-tests "$tmp/log" 121)
grep -q '^the test runner failed, not a test: nextest exited 102$' <<< "$said" || fail "extract 121: $said"

commit_rc() { # branch staged-rc expected [judge-rc]
    local rc=0
    git -C "$fx" checkout -q -B "$1"
    (cd "$fx" && FAKE_STAGED_RC=$2 FAKE_JUDGE_RC=${4:-0} just test-commit > "$tmp/out" 2>&1) || rc=$?
    [ "$rc" -eq "$3" ] || fail "test-commit on $1, tests exit $2: exit $rc, want $3: $(cat "$tmp/out")"
}
commit_rc feature 100 0
grep -q '^RED  tests: red tests committed on feature' "$tmp/out" || fail "test-commit: a red test off main is not said: $(cat "$tmp/out")"
grep -q '^judge asked: 100$' "$tmp/out" || fail "test-commit: red off main is not judged by red_judge: $(cat "$tmp/out")"
grep -qx 'test-staged asked: \[\]' "$tmp/out" || fail "test-commit off main: not the changed crates' own tests: $(cat "$tmp/out")"
commit_rc feature 120 0
# A red commit its judge refuses (`red_judge`: red tests it may not carry,
# such as a green test of a file it does not touch turned red) is refused,
# with the tests' own code, though it is off the default branch.
commit_rc feature 100 100 1
grep -q 'red tests cannot be committed: red_judge refused them' "$tmp/out" || fail "test-commit: the judge's refusal is not said: $(cat "$tmp/out")"
! grep -q '^RED' "$tmp/out" || fail "test-commit: a judged-out red commit is called RED: $(cat "$tmp/out")"
commit_rc feature 121 121
! grep -q 'RED' "$tmp/out" || fail "test-commit: a runner failure is called red: $(cat "$tmp/out")"
commit_rc feature 101 101
commit_rc feature 102 102
commit_rc main 100 100
grep -q 'red tests cannot be committed here' "$tmp/out" || fail "test-commit on main: $(cat "$tmp/out")"
grep -qx 'test-staged asked: \[dependents\]' "$tmp/out" || fail "test-commit on main: dependents not tested: $(cat "$tmp/out")"
! grep -q 'judge asked' "$tmp/out" || fail "test-commit on main: the judge was asked: $(cat "$tmp/out")"
commit_rc main 121 121
commit_rc main 0 0
! grep -q 'judge asked' "$tmp/out" || fail "test-commit: green tests were judged: $(cat "$tmp/out")"
echo "ok   tests-exit.selftest"
