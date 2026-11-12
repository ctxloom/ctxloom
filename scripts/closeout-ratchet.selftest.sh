#!/usr/bin/env bash
# Self-test of the ratchet (owner ruling 165): ratchet-judge.sh on fixture
# scenario lists, then closeout-ratchet.sh in a fixture repository whose
# justfile has stub gates (its `test` writes a fixture list where
# GHERKIN_RATCHET says, and exits as told).
#
# The judge: a slice whose red set is a subset of the base's passes and
# counts what turned green; an equal set passes; a new red scenario fails; a
# broken green one fails, each named; a missing list fails. The closeout: a
# subset passes; a failure outside the feature runner (another test, another
# gate) fails though the list is a subset; a new red fails; on a base with no
# red scenario (main) one red scenario fails, as the plain closeout would;
# mutants-batch gets merge-base(base, HEAD) and GHERKIN_RATCHET.
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL closeout-ratchet.selftest: $*" >&2; exit 1; }
list() { # <file> <red|green>:<id>...
    local file=$1 entry
    shift
    : > "$file"
    for entry in "$@"; do printf '%s\t%s\n' "${entry%%:*}" "${entry#*:}" >> "$file"; done
}
judge() { "$here/ratchet-judge.sh" "$tmp/base.list" "$tmp/slice.list" 2>&1; }

list "$tmp/base.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "green:f/b.feature :: three"

# A subset passes, and counts what it turned green.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 0 ] || fail "a subset was refused ($rc): $out"
grep -qx 'ok   ratchet (base 2 red; here 1 red, 1 turned green)' <<< "$out" || fail "subset line: $out"

# An equal set passes.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 0 ] || fail "an equal set was refused ($rc): $out"
grep -qx 'ok   ratchet (base 2 red; here 2 red, 0 turned green)' <<< "$out" || fail "equal line: $out"

# A new red scenario (the base has no such scenario) fails, named.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/c.feature :: four" "green:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 1 ] || fail "a new red passed ($rc): $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 0 broke green, 1 added red ' <<< "$out" || fail "new red line: $out"
grep -qx '  added red    f/c.feature :: four' <<< "$out" || fail "new red not named: $out"

# A broken green scenario fails, named.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 1 ] || fail "a broken green passed ($rc): $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 1 broke green, 0 added red ' <<< "$out" || fail "broken green line: $out"
grep -qx '  broke green  f/b.feature :: three' <<< "$out" || fail "broken green not named: $out"

# The names are capped, with a tail.
reds=()
for i in $(seq 1 5); do reds+=("red:f/n.feature :: s$i"); done
list "$tmp/slice.list" "${reds[@]}"
rc=0; out=$(RATCHET_LIST_MAX=2 judge) || rc=$?
[ "$rc" -eq 1 ] || fail "five new reds passed"
[ "$(grep -c '^  added red' <<< "$out")" -eq 2 ] || fail "not capped at 2: $out"
grep -q '^  +3 more: ' <<< "$out" || fail "no tail: $out"

# A missing list fails: a runner that listed nothing has shown nothing.
rm -f "$tmp/slice.list"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 1 ] || fail "a missing list passed"
grep -q '^FAIL ratchet: no scenario list ' <<< "$out" || fail "missing list line: $out"

# The closeout, in a fixture repository: base is a commit, HEAD a slice on it.
r=$tmp/r
git init -q "$r"
cat > "$r/justfile" <<'J'
build:
    @echo "ok   build (stub) 0.1s"
lint:
    @[ "${STUB_LINT:-0}" = 0 ] || { echo "FAIL lint: stub (full log: x)" >&2; exit 1; }; echo "ok   lint (stub) 0.1s"
test:
    @cp "$STUB_LIST" "$GHERKIN_RATCHET"; [ "${STUB_TEST_RC:-0}" = 0 ] || { echo "FAIL test: tests failed: 1 failed (full log: x)" >&2; exit "$STUB_TEST_RC"; }; echo "ok   test (stub) 0.1s"
mutants-batch base="":
    @echo "ok   mutants-batch (base=[{{base}}] ratchet=[${GHERKIN_RATCHET:-}]) 0.1s"
J
git -C "$r" add justfile
git -C "$r" -c user.name=t -c user.email=t@t commit -qm base
fork=$(git -C "$r" rev-parse HEAD)
git -C "$r" branch integration
echo x > "$r/x"
git -C "$r" add x
git -C "$r" -c user.name=t -c user.email=t@t commit -qm slice
closeout() { (cd "$r" && env RATCHET_GATES="build lint test mutants-batch" RATCHET_BASE_LIST="$tmp/base.list" \
    STUB_LIST="$tmp/slice.list" "$@" "$here/closeout-ratchet.sh" integration 2>&1); }

# A subset passes; mutants-batch is given the fork point and the list's path.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 0 ] || fail "closeout of a subset exited $rc: $out"
grep -q '^ok   ratchet (base 2 red; here 1 red, 1 turned green)' <<< "$out" || fail "no ratchet line: $out"
grep -q "base=\[$fork\] ratchet=\[$r/target/logs/ratchet/mutants.list\]" <<< "$out" || fail "mutants-batch args: $out"
grep -q '^ok   closeout-ratchet (4 gates; base [0-9a-f]\{7\}: 2 red, 1 turned green) ' <<< "$out" || fail "summary: $out"

# A failure outside the feature runner fails, though the list is a subset:
# another test (the test gate's own exit) ...
rc=0; out=$(closeout STUB_TEST_RC=100) || rc=$?
[ "$rc" -eq 1 ] || fail "a failing test outside the runner passed ($rc): $out"
grep -q '^ok   ratchet ' <<< "$out" || fail "the list was not judged beside the failure: $out"
grep -q '^FAIL closeout-ratchet: 1 failed: test ' <<< "$out" || fail "summary of another test: $out"
# ... and another gate.
rc=0; out=$(closeout STUB_LINT=1) || rc=$?
[ "$rc" -eq 1 ] || fail "a failing gate passed ($rc): $out"
grep -q '^FAIL closeout-ratchet: 1 failed: lint ' <<< "$out" || fail "summary of another gate: $out"
grep -q '^ok   mutants-batch' <<< "$out" || fail "a gate after the failing one did not run: $out"

# A new red fails the closeout.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 1 ] || fail "closeout with a broken green passed: $out"
grep -q '^FAIL closeout-ratchet: 1 failed: ratchet ' <<< "$out" || fail "summary of a new red: $out"

# On main, the base has no red scenario: the ratchet is the plain closeout.
list "$tmp/base.list" "green:f/a.feature :: one" "green:f/b.feature :: three"
list "$tmp/slice.list" "green:f/a.feature :: one" "green:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 0 ] || fail "an all-green slice on an all-green base failed: $out"
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 1 ] || fail "a red scenario passed on an all-green base: $out"

# Usage: no base is exit 2.
rc=0; out=$( (cd "$r" && "$here/closeout-ratchet.sh" 2>&1) ) || rc=$?
[ "$rc" -eq 2 ] || fail "no base exited $rc: $out"
echo "ok   closeout-ratchet.selftest"
