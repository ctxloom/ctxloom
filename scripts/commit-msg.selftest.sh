#!/usr/bin/env bash
# Self-test of the commit-msg gate (`just commit-msg-check <file>`, owner
# ruling 170): a message carrying a co-author trailer, in any case, on any
# line, is refused with a FAIL line that says so; every other message passes
# with one ok line. Text below git's scissors line (the diff `commit -v`
# appends) is not the message and is not judged.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
top=$(dirname "$here")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export GATE_LOG_DIR="$work/logs"

# The trailer's key, assembled so this file never holds it whole.
key="Co-Authored""-By"

n=0
expect() { # code, label, message
  local got=0 out
  n=$((n + 1))
  printf '%s' "$3" > "$work/msg$n"
  out=$(just --justfile "$top/justfile" commit-msg-check "$work/msg$n" 2>&1) || got=$?
  if [ "$got" != "$1" ]; then
    echo "selftest FAIL: $2 (exit $got, wanted $1)" >&2
    echo "$out" >&2
    exit 1
  fi
  case "$1" in
    0) [[ $out == "ok   commit-msg ("* ]] && [ "$(grep -c '' <<< "$out")" = 1 ] ||
         { echo "selftest FAIL: $2: a pass is one ok line, got: $out" >&2; exit 1; } ;;
    *) [[ $out == "FAIL commit-msg: "*"attribution trailer"* ]] ||
         { echo "selftest FAIL: $2: no FAIL line saying so, got: $out" >&2; exit 1; } ;;
  esac
}

expect 0 "a plain message" $'subject\n\nbody\n'
expect 0 "an empty message" ''
expect 0 "the words in prose, not a trailer" $'subject\n\nno co-authored-by trailers here\n'
expect 0 "another trailer" $'subject\n\nSigned-off-by: A <a@x>\n'
expect 1 "the trailer" $"subject

$key: Someone <s@x>
"
expect 1 "lower case" $"subject

${key,,}: someone <s@x>
"
expect 1 "upper case" $"subject

${key^^}: SOMEONE
"
expect 1 "indented, mid-body" $"subject

  $key: x
more body
"
expect 1 "on the subject line" "$key: x"
expect 1 "a space before the colon" $"subject

$key : x
"
expect 0 "below the scissors line" $"subject

# ------------------------ >8 ------------------------
+$key: in the diff
"

echo "ok   commit-msg selftest"
