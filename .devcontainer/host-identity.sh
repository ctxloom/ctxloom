#!/bin/sh
# devcontainer.json's initializeCommand, run on the HOST before the container
# starts: the git identity the host resolves for this checkout, name and email
# only, into .devcontainer/.identity (gitignored), which post-create.sh applies
# inside. Nothing else of the host's git configuration crosses.
set -eu
dir=$(dirname "$0")
{
    printf 'name=%s\n' "$(git config user.name || true)"
    printf 'email=%s\n' "$(git config user.email || true)"
} > "$dir/.identity"
