#!/bin/sh
# devcontainer.json's postCreateCommand, run as the dev user inside the
# container: the host's git identity (host-identity.sh), then the git hooks.
set -eu
dir=$(dirname "$0")
if [ -f "$dir/.identity" ]; then
    name=$(sed -n 's/^name=//p' "$dir/.identity")
    email=$(sed -n 's/^email=//p' "$dir/.identity")
    [ -n "$name" ] && git config --global user.name "$name"
    [ -n "$email" ] && git config --global user.email "$email"
fi
# gc never expires a worktree's record, whatever directory it cannot see
# from in here: the repository's .git is shared with the host's worktrees.
git config --global gc.worktreePruneExpire never
just hooks-install
