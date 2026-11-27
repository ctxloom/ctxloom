#!/usr/bin/env bash
# Self-test of handoff-check.sh against fixture trees: each in-memory hand-off
# it refuses (a lock around a collection, a Condvar, a channel sender carrying a
# value, a boxed closure in a field or a trait) is flagged in the two scanned
# trees and nowhere else; a clean tree, a doorbell, a lock around no collection,
# a test file and a listed exception pass; a stale or incomplete exception fails.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
check="$here/handoff-check.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fixture() { # name, source, [allow], [path under the fixture]
  local d="$work/$1" rel=${4:-crates/core/application/x/src/lib.rs}
  mkdir -p "$d/$(dirname "$rel")" "$d/scripts"
  printf '%s\n' "$2" > "$d/$rel"
  [ -n "${3:-}" ] && printf '%s\n' "$3" > "$d/scripts/handoff-allow.txt"
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

expect 0 "$(fixture clean 'struct S { n: u32 }')" clean
expect 1 "$(fixture mutex-map 'struct S { table: Arc<Mutex<BTreeMap<String, u32>>> }')" "Mutex of a BTreeMap"
expect 1 "$(fixture mutex-hashmap 'struct S { table: Mutex<HashMap<String, u32>> }')" "Mutex of a HashMap"
expect 1 "$(fixture rwlock-vec 'struct S { rows: std::sync::RwLock<Vec<String>> }')" "RwLock of a Vec"
expect 1 "$(fixture mutex-deque 'struct S { rows: Mutex<VecDeque<String>> }')" "Mutex of a VecDeque"
expect 1 "$(fixture static-vec 'static SAID: Mutex<Vec<String>> = Mutex::new(Vec::new());')" "a static"
expect 1 "$(fixture condvar 'struct S { changed: Condvar }')" "Condvar"
expect 1 "$(fixture sender 'struct S { tell: Sender<(String, u8)> }')" "a sender carrying a tuple"
expect 1 "$(fixture mpsc-sender 'struct S { tell: std::sync::mpsc::SyncSender<String> }')" "a SyncSender"
expect 1 "$(fixture watch 'struct S { done: tokio::sync::watch::Sender<u64> }')" "a watch Sender"
expect 1 "$(fixture boxed-fn 'struct S { cb: Box<dyn Fn(&str) + Send> }')" "Box<dyn Fn>"
expect 1 "$(fixture arc-fn 'struct S { cb: Arc<dyn Fn() -> u8 + Send + Sync> }')" "Arc<dyn Fn>"
expect 1 "$(fixture boxed-fnmut 'struct S { cb: Box<dyn FnMut(&str) + Send> }')" "Box<dyn FnMut>"
expect 1 "$(fixture trait-callback 'trait Port { fn watch(&self, on: Box<dyn Fn(u8) + Send>); }')" "a callback taken by a trait"
expect 1 "$(fixture root 'struct S { table: Mutex<Vec<String>> }' '' crates/root/lib/w/src/lib.rs)" "crates/root is scanned"
expect 0 "$(fixture doorbell 'struct S { bell: Sender<()> }')" "a doorbell, Sender<()>"
expect 0 "$(fixture mutex-option 'struct S { last: Mutex<Option<String>>, n: Mutex<u32> }')" "a lock around no collection"
expect 0 "$(fixture plain-box 'struct S { b: Box<dyn Iterator<Item = u8>> }')" "a Box of a trait object that is no closure"
expect 0 "$(fixture a-local 'fn f() { let t: Mutex<Vec<u8>> = Mutex::new(vec![]); let _ = t; }')" "a local, not a field"
expect 0 "$(fixture test-file 'struct S { table: Mutex<Vec<String>> }' '' crates/core/application/x/src/lib.test.rs)" "a *.test.rs file"
expect 0 "$(fixture outside 'struct S { table: Mutex<Vec<String>> }' '' crates/adapters/driven/y/src/lib.rs)" "a crate outside the two trees"
allow='crates/core/application/x/src/lib.rs | table: Mutex<Vec | debt: row 99, replaced by a stream tail (P4)'
expect 0 "$(fixture allowed 'struct S { table: Mutex<Vec<String>> }' "$allow")" "a listed exception"
expect 1 "$(fixture allowed-other 'struct S { rows: Mutex<Vec<String>> }' "$allow")" "an exception for another declaration, and the stale one"
expect 1 "$(fixture stale 'struct S { n: u32 }' "$allow")" "a stale exception"
expect 1 "$(fixture no-why 'struct S { table: Mutex<Vec<String>> }' 'crates/core/application/x/src/lib.rs | table: Mutex<Vec |')" "an exception with no why"
expect 1 "$(fixture debt-no-phase 'struct S { table: Mutex<Vec<String>> }' 'crates/core/application/x/src/lib.rs | table: Mutex<Vec | debt: row 99, replaced by a stream tail')" "a debt entry naming no phase"
expect 1 "$(fixture debt-p2 'struct S { table: Mutex<Vec<String>> }' 'crates/core/application/x/src/lib.rs | table: Mutex<Vec | debt: row 99, replaced by a stream tail (P2)')" "a debt entry naming P2"
expect 1 "$(fixture one-of-two 'struct S { table: Mutex<Vec<String>>, other: Condvar }' "$allow")" "one of two hits listed"
out=$("$check" "$(fixture counted 'struct S { table: Mutex<Vec<String>> }' "$allow")")
[[ $out == *"1 debt, 0 runner state"* ]] || { echo "selftest FAIL: the debt is counted: $out" >&2; exit 1; }
runner='crates/core/application/x/src/lib.rs | table: Mutex<Vec | its own scratch'
out=$("$check" "$(fixture counted-runner 'struct S { table: Mutex<Vec<String>> }' "$runner")")
[[ $out == *"0 debt, 1 runner state"* ]] || { echo "selftest FAIL: runner state is counted: $out" >&2; exit 1; }
echo "ok   handoff-check selftest"
