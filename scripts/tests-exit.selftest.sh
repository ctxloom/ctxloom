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
# are refused on every branch. The gates are gates/gates.just (or
# $GATES_JUST).
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
# what is checked.
cat > "$fx/justfile" <<'J'
set allow-duplicate-recipes := true
TOP := `git rev-parse --show-toplevel`
import 'gates/gates.just'
staged_tree := "../.{name}.staged"
feature_rules := ""
features_prepare := ""
mutation_thresholds := "crates=90"
workspace_wide := "Cargo.toml"
red_judge := "_judge"
[no-exit-message]
test-staged:
    @exit "${FAKE_STAGED_RC}"
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
! grep -q 'judge asked' "$tmp/out" || fail "test-commit on main: the judge was asked: $(cat "$tmp/out")"
commit_rc main 121 121
commit_rc main 0 0
! grep -q 'judge asked' "$tmp/out" || fail "test-commit: green tests were judged: $(cat "$tmp/out")"
echo "ok   tests-exit.selftest"
