#!/usr/bin/env bash
# Self-test of projector-check.sh against fixture trees. Crate `b` holds a
# projector (a Model, its Held handle `Bs`) and a pure fold `Fold`; the check
# refuses another projector's handle or model type named in a file that defines
# a projector (field, parameter, import, glob, alias, Held<..> of it), and
# passes pure fold and value-type reuse, a crate's own handle, a file that
# defines no projector, a test file, and a listed exception; a stale or
# incomplete exception fails, and a PENDING exception prints a note.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
check="$here/projector-check.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

B='use held_projection::{Held, Model};
pub struct B;
pub struct Bs(Held<B>);
impl Model for B {}
pub struct Fold { n: u64 }
pub struct Value { pub id: String }'

fixture() { # name, a's source, [allow], [path of a's file]
  local d="$work/$1" rel=${4:-crates/core/read-model/a/src/lib.rs}
  mkdir -p "$d/crates/core/read-model/b/src" "$d/$(dirname "$rel")" "$d/scripts"
  printf '%s\n' "$B" > "$d/crates/core/read-model/b/src/lib.rs"
  printf '%s\n' "$2" > "$d/$rel"
  [ -n "${3:-}" ] && printf '%s\n' "$3" > "$d/scripts/projector-allow.txt"
  echo "$d"
}
expect() { # code, dir, label
  local got=0 out
  out=$("$check" "$2" 2>&1) || got=$?
  if [ "$got" != "$1" ]; then
    echo "selftest FAIL: $3 (exit $got, wanted $1)" >&2
    echo "$out" >&2
    exit 1
  fi
}

A='pub struct A { n: u32 }
impl Model for A {}'
expect 0 "$(fixture clean "$A")" "a projector naming nothing foreign"
expect 0 "$(fixture pure-fold "use b::{Fold, Value};
pub struct A { fold: Fold, v: Value }
impl Model for A {}")" "pure fold and value types of another read model"
expect 0 "$(fixture own-handle "pub struct Own(Held<A>);
pub struct A;
impl Model for A {}")" "a crate's own handle"
expect 0 "$(fixture no-projector "use b::Bs;
pub struct Reader { bs: Bs }")" "a file that defines no projector may read a handle"
expect 1 "$(fixture field "use b::Bs;
pub struct A { other: Bs }
impl Model for A {}")" "another projector's handle as a field"
expect 1 "$(fixture param "pub struct A;
impl Model for A {}
fn fold(a: &b::B) {}")" "another projector's model type as a parameter"
expect 1 "$(fixture held-of "pub struct A { h: Held<b::B> }
impl Model for A {}")" "Held<..> of another projector's model"
expect 1 "$(fixture handler "pub struct A { bs: b::Bs }
impl<E> ProjectorHandler for A<E> {}" "" crates/core/application/a/src/lib.rs)" "a ProjectorHandler in the application ring"
expect 1 "$(fixture glob "use b::*;
pub struct A;
impl Model for A {}")" "a glob import of another projector's crate"
expect 1 "$(fixture alias "use b::Bs as Handle;
pub struct A;
impl Model for A {}")" "an alias of another projector's handle"
expect 0 "$(fixture test-file "use b::Bs;
pub struct A { other: Bs }
impl Model for A {}" "" crates/core/read-model/a/src/lib.test.rs)" "a *.test.rs file"
allow='crates/core/read-model/a/src/lib.rs | Bs | the fold needs b'
src='use b::Bs;
pub struct A { other: Bs }
impl Model for A {}'
expect 0 "$(fixture allowed "$src" "$allow")" "a listed exception"
expect 1 "$(fixture stale "$A" "$allow")" "a stale exception"
expect 1 "$(fixture no-why "$src" 'crates/core/read-model/a/src/lib.rs | Bs |')" "an exception with no why"
expect 1 "$(fixture one-of-two "use b::Bs;
pub struct A { other: Bs, v: b::B }
impl Model for A {}" "$allow")" "one of two hits listed"
pending='crates/core/read-model/a/src/lib.rs | Bs | PENDING OWNER RULING: fold the events itself'
out=$("$check" "$(fixture pending "$src" "$pending")" 2>&1)
[[ $out == *"note lint-projectors: PENDING"*"Bs"* ]] || { echo "selftest FAIL: a PENDING entry prints a note: $out" >&2; exit 1; }
[[ $out == *"1 pending"* ]] || { echo "selftest FAIL: pending counted: $out" >&2; exit 1; }
echo "ok   projector-check selftest"
