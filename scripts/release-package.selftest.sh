#!/usr/bin/env bash
# Self-test of release-package.sh on linux, against a small C program built
# with debug info: the stripped binary and its .debug file come out, carry
# the same build id, the binary runs and holds no debug section, and (when an
# llvm-symbolizer is found) main in the stripped binary resolves to its
# file:line through the debug file. An unknown os and a missing binary fail.
# Elsewhere than linux, or with no C compiler or objcopy, it says so and
# passes: the other platforms' packaging is proved by release.yml's run.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
package="$here/release-package.sh"
if [ "$(uname -s)" != Linux ] || ! command -v cc >/dev/null || ! command -v objcopy >/dev/null; then
    echo "release-package selftest: skipped (linux with cc and objcopy only)"
    exit 0
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() { echo "selftest FAIL: $*" >&2; exit 1; }

printf '#include <stdio.h>\nint main(void) {\n    puts("v0");\n    return 0;\n}\n' > "$work/main.c"
cc -g -O0 -Wl,--build-id -o "$work/prog" "$work/main.c"

"$package" linux "$work/prog" "$work/out" prog-v0-linux > "$work/listed" 2> "$work/said" ||
    fail "packaging a debug build failed: $(cat "$work/said")"
printf '%s\n' "$work/out/prog-v0-linux" "$work/out/prog-v0-linux.debug" | diff - "$work/listed" >/dev/null ||
    fail "the listed files are not the binary then its debug file: $(cat "$work/listed")"
readelf -W -S "$work/out/prog-v0-linux" | grep -E '\.debug_(info|line|str|abbrev)' >/dev/null && fail "the binary kept debug sections"
readelf -S "$work/out/prog-v0-linux" | grep '\.gnu_debuglink' >/dev/null || fail "the binary has no debug link"
readelf -S "$work/out/prog-v0-linux.debug" 2>/dev/null | grep '\.debug_line' >/dev/null || fail "the debug file has no line table"
if grep -q 'no llvm-symbolizer' "$work/said"; then
    echo "release-package selftest: no llvm-symbolizer, the lookup was not proved"
else
    grep -q 'main at 0x[0-9a-f]* of the stripped prog-v0-linux is .*main\.c:2' "$work/said" ||
        fail "main did not resolve to main.c:2: $(cat "$work/said")"
fi

expect_fail() { # label, args...
    local label=$1
    shift
    if "$package" "$@" >/dev/null 2>&1; then fail "$label passed"; fi
}
expect_fail "an unknown os" plan9 "$work/prog" "$work/out2" prog
expect_fail "a missing binary" linux "$work/nothing" "$work/out3" prog
cc -O0 -Wl,--build-id=none -o "$work/noid" "$work/main.c"
expect_fail "a binary with no build id" linux "$work/noid" "$work/out4" noid
echo "ok   release-package selftest"
