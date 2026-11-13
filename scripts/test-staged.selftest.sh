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
#   - an unstaged edit decides nothing: the staged tree is what is tested.
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
mutation_thresholds := "crates=90"
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
R
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
grep -q '^ok   test-staged (changed: a) ' "$tmp/out" \
    || fail "a change to a selects more than a: $(cat "$tmp/out")"

# So a change to the library that breaks only the binary's test passes here.
a 2
git -C "$fx" add crates/a/src/lib.rs
staged_rc "a change to a breaking only b" 0
grep -q '^ok   test-staged (changed: a) ' "$tmp/out" || fail "b was tested: $(cat "$tmp/out")"

# With `dependents` (the default branch's), the binary is selected, and red.
staged_rc "dependents of a change to a breaking b" 100 dependents
grep -q 'FAIL .*b::bin/b tests::sees_one' "$tmp/out" || fail "b's test is not named: $(cat "$tmp/out")"
a '1 + 0'
git -C "$fx" add crates/a/src/lib.rs
staged_rc "dependents of a change to a" 0 dependents
grep -q '^ok   test-staged (changed: a; with dependents: a b) ' "$tmp/out" \
    || fail "the dependent binary is not selected: $(cat "$tmp/out")"
git -C "$fx" reset -q
a 1

# A change to the binary selects the binary.
printf '// touched\n' >> "$fx/crates/b/src/main.rs"
git -C "$fx" add crates/b/src/main.rs
staged_rc "a change to b" 0
grep -q '^ok   test-staged (changed: b) ' "$tmp/out" || fail "a change to b does not select b: $(cat "$tmp/out")"
git -C "$fx" reset -q
git -C "$fx" checkout -q -- crates/b/src/main.rs

# A staged workspace-wide file tests the whole workspace.
printf '# touched\n' >> "$fx/Cargo.toml"
git -C "$fx" add Cargo.toml
staged_rc "a workspace-wide file" 0
grep -q '^ok   test-staged (workspace: Cargo.toml is staged) ' "$tmp/out" \
    || fail "a workspace-wide file does not test the workspace: $(cat "$tmp/out")"
git -C "$fx" reset -q
git -C "$fx" checkout -q -- Cargo.toml

# The staged tree decides: an unstaged fix changes nothing.
a 2
git -C "$fx" add crates/a/src/lib.rs
staged_rc "a change to a breaking b, dependents" 100 dependents
a 1
staged_rc "an unstaged fix" 100 dependents

echo "ok   test-staged.selftest"
