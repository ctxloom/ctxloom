#!/usr/bin/env bash
# Self-test of path-deps-check.sh against temp git repositories: a dependency
# or [patch]/[replace] entry whose path leaves the repository (or is absolute)
# fails, a tracked .cargo/config.toml fails, and everything else passes.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
check="$here/path-deps-check.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fixture() { # name, then pairs of: file content  (staged, not committed)
  local d="$work/$1" f
  shift
  mkdir -p "$d"
  git -C "$d" init -q
  while [ "$#" -ge 2 ]; do
    f=$1
    mkdir -p "$d/$(dirname "$f")"
    printf '%s\n' "$2" > "$d/$f"
    git -C "$d" add -f "$f"
    shift 2
  done
  echo "$d"
}
expect() { # code, dir, label
  local got=0
  "$check" "$2" >/dev/null 2>&1 || got=$?
  if [ "$got" != "$1" ]; then echo "selftest FAIL: $3 (exit $got, wanted $1)" >&2; exit 1; fi
}

pkg='[package]
name = "a"
version = "0.1.0"'

expect 0 "$(fixture clean Cargo.toml "$pkg")" "clean tree"
expect 0 "$(fixture none)" "nothing staged"
expect 1 "$(fixture escape Cargo.toml "$pkg
[dependencies]
x = { path = \"../escape\" }")" "dependencies path = ../escape"
expect 1 "$(fixture escape-dev crates/a/Cargo.toml "$pkg
[dev-dependencies]
x = { path = \"../../../outside\" }")" "dev-dependencies escaping from a crate"
expect 1 "$(fixture escape-build Cargo.toml "$pkg
[build-dependencies.x]
path = \"../x\"")" "build-dependencies sub-table"
expect 1 "$(fixture escape-ws Cargo.toml '[workspace.dependencies]
x = { path = "../x" }')" "workspace.dependencies"
expect 1 "$(fixture escape-target Cargo.toml "$pkg
[target.'cfg(unix)'.dependencies]
x = { path = \"../x\" }")" "target-specific dependencies"
expect 1 "$(fixture absolute Cargo.toml "$pkg
[dependencies]
x = { path = \"/opt/x\" }")" "absolute path"
expect 1 "$(fixture patch-crates Cargo.toml "$pkg
[patch.crates-io]
x = { path = \"../x\" }")" "[patch.crates-io]"
expect 1 "$(fixture patch-git Cargo.toml "$pkg
[patch.\"https://github.com/ctxloom/ctxloom-lib\"]
x = { path = \"../ctxloom-lib/x\" }")" "[patch.\"https://...\"]"
expect 1 "$(fixture replace Cargo.toml "$pkg
[replace]
\"x:0.1.0\" = { path = \"../x\" }")" "[replace]"
expect 1 "$(fixture config-tracked .cargo/config.toml '# local')" "tracked .cargo/config.toml"
expect 1 "$(fixture config-bare .cargo/config '# local')" "tracked .cargo/config"
expect 1 "$(fixture config-patch .cargo/other.toml '[patch.crates-io]
x = { path = "../x" }')" "a .cargo/*.toml patch outside"
expect 0 "$(fixture config-inside .cargo/other.toml '[patch.crates-io]
x = { path = "vendor/x" }')" "a .cargo/*.toml patch inside (relative to the repository root)"
expect 1 "$(fixture config-paths .cargo/other.toml 'paths = ["../x"]')" "a .cargo paths override outside"
expect 0 "$(fixture local-deps-sample .cargo/local-deps.toml '[patch.crates-io]
x = { path = "../x" }')" "the untracked-by-config sample is not a cargo config"
expect 0 "$(fixture inside Cargo.toml "$pkg
[dependencies]
x = { path = \"crates/x\" }
y = { path = \"./crates/y/../y\" }")" "inside paths"
expect 0 "$(fixture inside-up crates/a/Cargo.toml "$pkg
[dependencies]
x = { path = \"../b\" }")" "a sibling crate in the repository"
expect 0 "$(fixture git-rev Cargo.toml "$pkg
[dependencies]
x = { git = \"https://example.com/x\", rev = \"0123456789abcdef0123456789abcdef01234567\" }")" "git + rev"
expect 0 "$(fixture comment Cargo.toml "$pkg
# x = { path = \"../x\" }
[package.metadata]
note = \"path = '../x'\"
[dependencies]
y = \"1\"")" "a comment and a string containing path ="
expect 0 "$(fixture metadata Cargo.toml "$pkg
[package.metadata.foo]
path = \"../elsewhere\"")" "a path key that is not a dependency"
expect 1 "$(fixture broken Cargo.toml '[dependencies
x = ')" "a manifest that does not parse"
expect 0 "$(fixture other-file docs/notes.toml '[dependencies]
x = { path = "../x" }')" "a toml that is not a manifest"

d=$(fixture staged-only Cargo.toml "$pkg")
git -C "$d" -c user.email=a@b -c user.name=n commit -q -m base
printf '%s\n[dependencies]\nx = { path = "../x" }\n' "$pkg" > "$d/Cargo.toml"
expect 0 "$d" "an unstaged working-copy change is not judged"
git -C "$d" add Cargo.toml
expect 1 "$d" "the same change staged"

out=$("$check" "$(fixture message Cargo.toml "$pkg
[dependencies]
x = { path = \"../escape\" }")" 2>&1 || true)
case $out in
  *"Cargo.toml:5"*"../escape"*"gitignored .cargo/config.toml"*) ;;
  *) echo "selftest FAIL: the failure does not name the manifest, line, path and fix: $out" >&2; exit 1 ;;
esac
echo "ok   path-deps selftest"
