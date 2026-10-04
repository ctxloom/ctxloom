#!/usr/bin/env bash
# Writes the module's source, as a tar, to stdout — the copy a container
# mutation run mutates instead of the working tree.
#
# Usage: mutation_source.sh   (from anywhere inside the checkout)
#
# WHY A COPY: a mutation run must be physically unable to write the working
# tree, whatever the tool does. Bound into the container, the checkout is
# writable by the tool and every test it runs, and whether a killed run leaves
# a mutant behind (SIGKILL runs no defer or t.Cleanup) would rest on the tool's
# internals. Streamed in, the copy lives in the container's own layer and
# `--rm` discards it.
#
# WHAT IS THE SOURCE: tracked and untracked files git does not ignore, plus
# ignored Go files that are not inside an ignored directory — the generated
# code the build needs. Ignored directories (build output, caches, nested
# repositories) are what make a checkout gigabytes, and none of them is source.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
{
    git ls-files -z --cached --others --exclude-standard
    git ls-files -z --others --ignored --exclude-standard --directory -- '*.go'
} | while IFS= read -r -d '' f; do
    # A directory entry is a nested repository or a collapsed ignored dir; a
    # missing path is a tracked file deleted in the working tree.
    case "$f" in */) continue ;; esac
    if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\0' "$f"; fi
done | tar --null --no-recursion -T - -cf -
