#!/usr/bin/env bash
# `test-staged` tests the crates a staged change touches, their OWN tests
# only (owner ruling of 2026-11-13: fast hooks, acceptance at landing), in the
# staged tree; `test-staged dependents` (what `test-commit` runs on the
# default branch) adds the workspace crates that depend on them. Checked on a
# fixture workspace of a library `a` and a binary `b` that depends on it as
# ctxloom's binary depends on its libraries (`a.workspace = true`, the path
# in [workspace.dependencies]).
#   - a staged change to `a` alone selects `a` alone (the pass line says so),
#     so one that breaks only `b`'s test passes;
#   - a staged change to `b` selects `b`;
#   - with `dependents`, the change to `a` selects `b` too, and the one that
#     breaks `b`'s test fails the gate red (100), naming `b`'s test;
#   - a staged workspace-wide file tests the whole workspace;
#   - an unstaged edit decides nothing: the staged tree is what is tested;
#   - `staged_tests_filter` narrows the run (2026-11-18): the fixture's
#     filter.sh, as ctxloom's scripts/staged-features.sh, keeps every test
#     but `b`'s `feat_*` ones, and of those the ones whose `feats/<n>.txt`
#     is staged; `feat_y` always fails, so a run that is not narrowed (no
#     filter, or `just test`, which is never narrowed) is red with it, and
#     a staged `feats/x.txt` runs `feat_x`, red when its file is.
# The gates are gates/gates.just (or $GATES_JUST).
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
here=$(cd "$(dirname "$0")" && pwd)
gates=${GATES_JUST:-$here/../gates/gates.just}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL test-staged.selftest: $*" >&2; exit 1; }

fx=$tmp/fx
mkdir -p "$fx/gates" "$fx/crates/a/src" "$fx/crates/b/src"
cp "$gates" "$fx/gates/gates.just"
cp "$(dirname "$gates")/drop-worktree.sh" "$fx/gates/drop-worktree.sh"
cp "$(dirname "$gates")/memory-cap.sh" "$fx/gates/memory-cap.sh"
cp "$(dirname "$gates")/tests-lock.sh" "$fx/gates/tests-lock.sh"
cp "$(dirname "$gates")/red-sanity.sh" "$fx/gates/red-sanity.sh"
# The fixture's workspace runs take a test lock of their own, never the
# machine's (gates/tests-lock.sh), and are inside no other run's.
export TESTS_LOCK_FILE=$tmp/workspace-tests.lock
unset GATES_TESTS_LOCK_HELD
cat > "$fx/Cargo.toml" <<'T'
[workspace]
members = ["crates/a", "crates/b"]
resolver = "2"

[workspace.dependencies]
a = { path = "crates/a" }
T
cat > "$fx/crates/a/Cargo.toml" <<'T'
[package]
name = "a"
version = "0.1.0"
edition = "2021"
T
cat > "$fx/crates/b/Cargo.toml" <<'T'
[package]
name = "b"
version = "0.1.0"
edition = "2021"

[dependencies]
a.workspace = true
T
cat > "$fx/justfile" <<'J'
TOP := `git rev-parse --show-toplevel`
import 'gates/gates.just'
staged_tree := "../.{name}.staged"
feature_rules := ""
features_prepare := ""
red_judge := ""
staged_tests_filter := env("FIXTURE_FILTER", "filter.sh")
mutation_thresholds := "crates=90"
mutants_memory_max := "16G"
mutants_swap_max := "2G"
tests_memory_max := "none"
tests_swap_max := "0"
workspace_wide := "Cargo.toml"
J
a() { printf 'pub fn one() -> u32 { %s }\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn own() { assert!(super::one() > 0); }\n}\n' "$1" > "$fx/crates/a/src/lib.rs"; }
a 1
cat > "$fx/crates/b/src/main.rs" <<'R'
fn main() { println!("{}", a::one()); }
#[cfg(test)]
mod tests {
    #[test]
    fn sees_one() { assert_eq!(a::one(), 1); }
}
#[cfg(test)]
mod feats {
    #[test]
    fn feat_x() { assert_eq!(include_str!("../feats/x.txt"), "ok\n"); }
    #[test]
    fn feat_y() { panic!("feat_y ran, though feats/y.txt is not staged"); }
}
R
mkdir -p "$fx/crates/b/feats"
printf 'ok\n' > "$fx/crates/b/feats/x.txt"
printf 'ok\n' > "$fx/crates/b/feats/y.txt"
cat > "$fx/filter.sh" <<'F'
#!/usr/bin/env bash
set="not test(/^feats::/)"
while IFS= read -r path; do
    case "$path" in
        crates/b/feats/*.txt) name=${path#crates/b/feats/}; set="$set | test(=feats::feat_${name%.txt})" ;;
    esac
done
echo "$set"
F
chmod +x "$fx/filter.sh"
export CARGO_TARGET_DIR=$tmp/target CARGO_TERM_COLOR=never
git -C "$fx" init -q -b main
printf 'target/\nCargo.lock\n' > "$fx/.gitignore"
git -C "$fx" add -A
git -C "$fx" -c user.email=t@e -c user.name=t commit -q -m init
(cd "$fx" && cargo generate-lockfile -q --offline)

staged_rc() { # name expected [scope]
    local rc=0
    (cd "$fx" && just test-staged ${3:+"$3"} > "$tmp/out" 2>&1) || rc=$?
    [ "$rc" -eq "$2" ] || fail "$1: exit $rc, want $2: $(tail -n 20 "$tmp/out")"
}

# A change to the library alone selects the library alone: its dependent
# binary's tests are the landing gate's.
a '1 + 0'
git -C "$fx" add crates/a/src/lib.rs
staged_rc "a change to a" 0
grep -q '^ok   test-staged (changed: a; narrowed by filter.sh) ' "$tmp/out" \
    || fail "a change to a selects more than a: $(cat "$tmp/out")"

# So a change to the library that breaks only the binary's test passes here.
a 2
git -C "$fx" add crates/a/src/lib.rs
staged_rc "a change to a breaking only b" 0
grep -q '^ok   test-staged (changed: a; narrowed by filter.sh) ' "$tmp/out" || fail "b was tested: $(cat "$tmp/out")"

# With `dependents` (the default branch's), the binary is selected, and red.
staged_rc "dependents of a change to a breaking b" 100 dependents
grep -q 'FAIL .*b::bin/b tests::sees_one' "$tmp/out" || fail "b's test is not named: $(cat "$tmp/out")"
a '1 + 0'
git -C "$fx" add crates/a/src/lib.rs
staged_rc "dependents of a change to a" 0 dependents
grep -q '^ok   test-staged (changed: a; with dependents: a b; narrowed by filter.sh) ' "$tmp/out" \
    || fail "the dependent binary is not selected: $(cat "$tmp/out")"
git -C "$fx" reset -q
a 1

# A change to the binary selects the binary.
printf '// touched\n' >> "$fx/crates/b/src/main.rs"
git -C "$fx" add crates/b/src/main.rs
staged_rc "a change to b" 0
grep -q '^ok   test-staged (changed: b; narrowed by filter.sh) ' "$tmp/out" || fail "a change to b does not select b: $(cat "$tmp/out")"
git -C "$fx" reset -q
git -C "$fx" checkout -q -- crates/b/src/main.rs

# A staged workspace-wide file tests the whole workspace.
printf '# touched\n' >> "$fx/Cargo.toml"
git -C "$fx" add Cargo.toml
staged_rc "a workspace-wide file" 0
grep -q '^ok   test-staged (workspace: Cargo.toml is staged; narrowed by filter.sh) ' "$tmp/out" \
    || fail "a workspace-wide file does not test the workspace: $(cat "$tmp/out")"
git -C "$fx" reset -q
git -C "$fx" checkout -q -- Cargo.toml

# The staged tree decides: an unstaged fix changes nothing.
a 2
git -C "$fx" add crates/a/src/lib.rs
staged_rc "a change to a breaking b, dependents" 100 dependents
a 1
staged_rc "an unstaged fix" 100 dependents

git -C "$fx" reset -q
a 1

# The narrowing: a change to b runs b's tests but its unstaged feats.
printf '// touched\n' >> "$fx/crates/b/src/main.rs"
git -C "$fx" add crates/b/src/main.rs
staged_rc "a change to b, narrowed" 0
grep -q '^ok   test-staged (changed: b; narrowed by filter.sh) ' "$tmp/out" \
    || fail "the narrowing is not said: $(cat "$tmp/out")"
# Not narrowed, the same change runs feat_y, red.
rc=0
(cd "$fx" && FIXTURE_FILTER= just test-staged > "$tmp/out" 2>&1) || rc=$?
[ "$rc" -eq 100 ] || fail "with no filter, feat_y did not run red: exit $rc: $(tail -n 20 "$tmp/out")"
grep -q 'FAIL .*b::bin/b feats::feat_y' "$tmp/out" || fail "feat_y is not named: $(cat "$tmp/out")"
# The narrowing holds the dependents' run and the workspace's too.
git -C "$fx" reset -q
git -C "$fx" checkout -q -- crates/b/src/main.rs
a '1 + 0'
git -C "$fx" add crates/a/src/lib.rs
staged_rc "dependents of a change to a, narrowed" 0 dependents
git -C "$fx" reset -q
a 1
printf '# touched\n' >> "$fx/Cargo.toml"
git -C "$fx" add Cargo.toml
staged_rc "a workspace-wide file, narrowed" 0
grep -q '^ok   test-staged (workspace: Cargo.toml is staged; narrowed by filter.sh) ' "$tmp/out" \
    || fail "the workspace run is not narrowed: $(cat "$tmp/out")"
git -C "$fx" reset -q
git -C "$fx" checkout -q -- Cargo.toml
# A staged feats file runs its test: red when it is.
printf 'bad\n' > "$fx/crates/b/feats/x.txt"
git -C "$fx" add crates/b/feats/x.txt
staged_rc "a staged feats/x.txt" 100
grep -q 'FAIL .*b::bin/b feats::feat_x' "$tmp/out" || fail "the staged feats/x.txt's test did not run: $(cat "$tmp/out")"
! grep -q 'feats::feat_y' "$tmp/out" || fail "feat_y ran beside feat_x: $(cat "$tmp/out")"
git -C "$fx" reset -q
printf 'ok\n' > "$fx/crates/b/feats/x.txt"
# `just test` is never narrowed: feat_y runs, red.
rc=0
(cd "$fx" && just test > "$tmp/out" 2>&1) || rc=$?
[ "$rc" -eq 100 ] || fail "just test was narrowed: exit $rc: $(tail -n 20 "$tmp/out")"
grep -q 'FAIL .*b::bin/b feats::feat_y' "$tmp/out" || fail "just test: feat_y is not named: $(cat "$tmp/out")"

echo "ok   test-staged.selftest"
