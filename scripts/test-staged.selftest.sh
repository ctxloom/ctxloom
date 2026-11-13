#!/usr/bin/env bash
# `test-staged` tests a staged change's crate AND the workspace crates that
# depend on it, in the staged tree: checked on a fixture workspace of a
# library `a` and a binary `b` that depends on it as ctxloom's binary depends
# on its libraries (`a.workspace = true`, the path in [workspace.dependencies]).
#   - a staged change to `a` alone selects `b` too (the pass line names it);
#   - one that breaks `b`'s test, `a`'s own passing, fails the gate red (100),
#     naming `b`'s test;
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

staged_rc() { # name expected
    local rc=0
    (cd "$fx" && just test-staged > "$tmp/out" 2>&1) || rc=$?
    [ "$rc" -eq "$2" ] || fail "$1: exit $rc, want $2: $(tail -n 20 "$tmp/out")"
}

# A change to the library alone selects its dependent binary.
a '1 + 0'
git -C "$fx" add crates/a/src/lib.rs
staged_rc "a change to a" 0
grep -q '^ok   test-staged (changed: a; with dependents: a b)' "$tmp/out" \
    || fail "the dependent binary is not selected: $(cat "$tmp/out")"

# A change to the library that breaks only the binary's test is red.
a 2
git -C "$fx" add crates/a/src/lib.rs
staged_rc "a change to a breaking b" 100
grep -q 'FAIL .*b::bin/b tests::sees_one' "$tmp/out" || fail "b's test is not named: $(cat "$tmp/out")"

# The staged tree decides: an unstaged fix changes nothing.
a 1
staged_rc "an unstaged fix" 100

echo "ok   test-staged.selftest"
