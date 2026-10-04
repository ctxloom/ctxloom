#!/usr/bin/env bash
# Prints the `--mount` arguments the dev container needs for THIS checkout's
# git data, one argument per line, for the justfile's _run recipe.
#
# Usage: devcontainer-git-mounts.sh <checkout> <workspace-dst> <mask-dir>
#
#   <checkout>       the checkout _run mounts (its top level)
#   <workspace-dst>  where _run mounts it inside the container
#   <mask-dir>       an empty directory the CALLER owns and removes after the
#                    container exits; it is bound read-only over the
#                    worktrees/ registry
#
# The layout is the agent call site's — isolation.gitDirMounts and
# gitRegistryMask own the rationale (docs/adr/0034): the common dir read-write,
# its worktrees/ registry masked, this checkout's own admin dir mounted back.
# The registry belongs to OTHER checkouts, none of which is mounted, so an
# unmasked `git worktree prune` (or gc's auto-prune) in the container deletes
# their registrations on the host.
#
# A host path maps into the container at <workspace-dst> when it lies inside
# the checkout (a main checkout's .git), and at its identical absolute path
# otherwise (a linked worktree's common dir — its .git pointer is absolute).
#
# --mount, not -v: the `-v src:dst:ro` form mis-parses a destination ending in
# `.git` (see docs/adr/0034).
set -euo pipefail

if [ "$#" -ne 3 ]; then
    echo "usage: $0 <checkout> <workspace-dst> <mask-dir>" >&2
    exit 2
fi
top=$(cd "$1" && pwd -P)
dst=$2
mask=$3

cd "$top"
common=$(cd "$(git rev-parse --git-common-dir)" && pwd -P)
admin=$(cd "$(git rev-parse --git-dir)" && pwd -P)
registry="$common/worktrees"

target() {
    case "$1" in
        "$top") printf '%s\n' "$dst" ;;
        "$top"/*) printf '%s\n' "$dst/${1#"$top"/}" ;;
        *) printf '%s\n' "$1" ;;
    esac
}

bind() { # src dst [readonly]
    printf -- '--mount\ntype=bind,src=%s,dst=%s%s\n' "$1" "$2" "${3:+,readonly}"
}

case "$common" in
    "$top" | "$top"/*) ;;
    *) bind "$common" "$(target "$common")" ;;
esac

[ -d "$registry" ] || exit 0

own=""
if [ "$(dirname "$admin")" = "$registry" ]; then
    own=$admin
    # Created beforehand: the runtime cannot create a mountpoint inside a
    # read-only source, and a rootful daemon would create it as root.
    mkdir -p "$mask/$(basename "$own")"
fi
bind "$mask" "$(target "$registry")" readonly
if [ -n "$own" ]; then
    bind "$own" "$(target "$own")"
    # The registration's back-pointer names the checkout at its HOST path, and
    # the checkout is mounted at <workspace-dst>: without this bind prune
    # finds the back-pointer dangling and empties this checkout's own admin
    # dir through the read-write mount above.
    back=$(dirname "$(cat "$own/gitdir")")
    bind "$back" "$back"
fi
