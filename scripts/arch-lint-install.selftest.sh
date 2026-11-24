#!/usr/bin/env bash
# Self-test of arch-lint-install.sh with a fake cargo on PATH: an installed
# binary is left alone, a hanging cargo is cut at the deadline with the FAIL
# line, a failing cargo says its exit, git is told never to prompt, and a
# successful install lands under the root it was given.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
inst="$here/arch-lint-install.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
fail() { echo "selftest FAIL: $1" >&2; exit 1; }

fake() { # body of the fake cargo
  printf '#!/bin/sh\n%s\n' "$1" > "$work/bin/cargo"
  chmod +x "$work/bin/cargo"
}
run() { # root; env and exit pass through
  PATH="$work/bin:$PATH" "$inst" "$1" rev123 "$work/log/install.log"
}

# present: cargo is never run
mkdir -p "$work/have/bin"; : > "$work/have/bin/arch-lint"; chmod +x "$work/have/bin/arch-lint"
fake 'echo ran > '"$work/ran"'; exit 1'
run "$work/have" || fail "an installed binary was not left alone"
[ ! -e "$work/ran" ] || fail "cargo ran although arch-lint was installed"

# hang: cut at the deadline, FAIL line, no prompt variables
fake 'echo "$GIT_TERMINAL_PROMPT|$GIT_ASKPASS|$SSH_ASKPASS" > '"$work/env"'; sleep 60'
out=$(ARCH_LINT_TIMEOUT=1 run "$work/hang" 2>&1) && fail "a hanging install passed"
case $out in
  "FAIL lint-arch: arch-lint could not be installed (no result within 1s; log: $work/log/install.log)"*) ;;
  *) fail "hang said: $out" ;;
esac
[ "$(cat "$work/env")" = "0||" ] || fail "git may prompt: $(cat "$work/env")"

# failure: its exit is said, its log kept
fake 'echo boom; exit 101'
out=$(run "$work/bad" 2>&1) && fail "a failing install passed"
case $out in *"cargo install exited 101"*boom*) ;; *) fail "failure said: $out" ;; esac

# success: lands under the root given
fake 'while [ "$1" != --root ]; do shift; done; mkdir -p "$2/bin"; : > "$2/bin/arch-lint"; chmod +x "$2/bin/arch-lint"'
run "$work/good" || fail "a good install failed"
[ -x "$work/good/bin/arch-lint" ] || fail "not installed under the root"
echo "ok arch-lint-install selftest"
