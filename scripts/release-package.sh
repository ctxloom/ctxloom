#!/usr/bin/env bash
# Package a release binary NEXT TO its debug file (owner ruling 2026-11-12).
# The release profile keeps the debug info (`debug = "line-tables-only"`,
# `strip = "none"`: cargo's own strip would delete what the debug file needs),
# and this step splits it off, per platform:
#
#   linux    <name> stripped (--strip-all), with a .gnu_debuglink to
#            <name>.debug, the debug sections alone, zlib-compressed; the two
#            must carry the same build id, and the stripped binary must still
#            run (`version`).
#   macos    <name>.dSYM (cargo's, split-debuginfo packed, else dsymutil's),
#            zipped as <name>.dSYM.zip; the binary stripped after it and
#            signed again ad hoc (a stripped arm64 binary's signature no
#            longer holds); the two must carry the same UUID.
#   windows  <name>.exe as built (MSVC keeps its debug info in the PDB) with
#            <name>.pdb, the PDB cargo wrote beside it.
#
# When an llvm-symbolizer is found (LLVM_SYMBOLIZER names one, else
# llvm-symbolizer or llvm-symbolizer-<n> on PATH), the linux package is proved
# usable: the address of the program's main (a Rust crate's `main`, else C's)
# in the STRIPPED binary must resolve to a file:line through its debug file. With none found the proof is skipped,
# said on stderr.
#
# Usage: release-package.sh <linux|macos|windows> <built binary> <out dir> <name>
# stdout: the packaged files' paths, one per line (the binary first); exit 1
# on a failure, its reason on stderr.
set -euo pipefail
os=${1:?usage: release-package.sh <linux|macos|windows> <binary> <out dir> <name>}
bin=${2:?usage: release-package.sh <linux|macos|windows> <binary> <out dir> <name>}
out=${3:?usage: release-package.sh <linux|macos|windows> <binary> <out dir> <name>}
name=${4:?usage: release-package.sh <linux|macos|windows> <binary> <out dir> <name>}

fail() { echo "release-package: $*" >&2; exit 1; }

[ -f "$bin" ] || fail "no binary at $bin"
mkdir -p "$out"
out=$(cd "$out" && pwd)

symbolizer() {
    if [ -n "${LLVM_SYMBOLIZER:-}" ]; then echo "$LLVM_SYMBOLIZER"; return; fi
    command -v llvm-symbolizer && return
    compgen -c llvm-symbolizer- | sort -t- -k3 -n | tail -n 1
}

linux() {
    local packed="$out/$name" debug="$out/$name.debug"
    cp "$bin" "$packed"
    objcopy --only-keep-debug --compress-debug-sections=zlib "$packed" "$debug"
    # The link is the debug file's base name, so run where it is.
    (cd "$out" && objcopy --strip-all --add-gnu-debuglink="$name.debug" "$name")
    build_id() { readelf -n "$1" 2>/dev/null | awk '/Build ID:/ { print $3 }'; }
    local stripped_id debug_id
    stripped_id=$(build_id "$packed")
    debug_id=$(build_id "$debug")
    [ -n "$stripped_id" ] || fail "$packed carries no build id"
    [ "$stripped_id" = "$debug_id" ] ||
        fail "build ids differ: $packed $stripped_id, $debug ${debug_id:-none}"
    # (.debug_gdb_scripts is loaded, not debug info: it stays.)
    readelf -W -S "$packed" | grep -E '\.debug_(info|line|str|abbrev|ranges|rnglists)' >/dev/null &&
        fail "$packed still holds debug sections"
    readelf -W -S "$debug" 2>/dev/null | grep '\.debug_line' >/dev/null || fail "$debug holds no line table"
    "$packed" version >/dev/null || fail "the stripped $packed does not run"
    local llvm
    llvm=$(symbolizer || true)
    if [ -z "$llvm" ]; then
        echo "release-package: no llvm-symbolizer; the debug file's lookup is not proved" >&2
    else
        local main where
        # A Rust program's own `<crate>::main` (the C `main` rustc makes has
        # no line), else a C program's `main`.
        main=$(nm --defined-only "$debug" | awk '
            $3 ~ /^_ZN[0-9]+[A-Za-z0-9_]+4main17h[0-9a-f]+E$/ && !rust { rust = $1 }
            $3 == "main" && !c { c = $1 }
            END { print (rust != "" ? rust : c) }')
        [ -n "$main" ] || fail "$debug has no main"
        where=$("$llvm" --obj="$packed" "0x$main")
        printf '%s\n' "$where" | grep -E '^[^?].*:[0-9]+(:[0-9]+)?$' >/dev/null ||
            fail "$llvm resolved main (0x$main) of the stripped $packed to no file:line: $where"
        echo "release-package: main at 0x$main of the stripped $name is $(printf '%s' "$where" | tail -n 1)" >&2
    fi
    echo "$packed"
    echo "$debug"
}

macos() {
    local packed="$out/$name" dsym="$out/$name.dSYM"
    rm -rf "$dsym"
    if [ -d "$bin.dSYM" ]; then
        cp -R "$bin.dSYM" "$dsym"
    else
        dsymutil "$bin" -o "$dsym"
    fi
    cp "$bin" "$packed"
    strip "$packed"
    codesign --force --sign - "$packed"
    uuid() { dwarfdump --uuid "$1" | awk '{ print $2 }' | sort; }
    local bin_uuid dsym_uuid
    bin_uuid=$(uuid "$packed")
    dsym_uuid=$(uuid "$dsym")
    [ -n "$bin_uuid" ] || fail "$packed carries no UUID"
    [ "$bin_uuid" = "$dsym_uuid" ] || fail "UUIDs differ: $packed $bin_uuid, $dsym ${dsym_uuid:-none}"
    "$packed" version >/dev/null || fail "the stripped $packed does not run"
    rm -f "$dsym.zip"
    (cd "$out" && ditto -c -k --keepParent "$name.dSYM" "$name.dSYM.zip")
    rm -rf "$dsym"
    echo "$packed"
    echo "$dsym.zip"
}

windows() {
    local exe=$bin pdb
    [ -f "$exe.exe" ] && exe=$exe.exe
    pdb="$(dirname "$exe")/$(basename "$exe" .exe).pdb"
    [ -s "$pdb" ] || fail "no PDB beside $exe (looked for $pdb)"
    cp "$exe" "$out/$name.exe"
    cp "$pdb" "$out/$name.pdb"
    echo "$out/$name.exe"
    echo "$out/$name.pdb"
}

case "$os" in
    linux | macos | windows) "$os" ;;
    *) fail "unknown os '$os': linux, macos or windows" ;;
esac
