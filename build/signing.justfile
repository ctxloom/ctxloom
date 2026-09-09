# Signing vocabulary for the ctxloom family — the ONE definition of the
# identity, the namespace, the key and the sign/verify commands.
#
# Imported by BOTH roots (justfile and justfile.container) rather than living in
# either, and that is load-bearing: `install` signs on the HOST while the
# release pipeline signs inside a container that never sees the host justfile.
# Two copies of these terms would let a released binary be signed under terms
# that differ from a locally installed one, and the failure surfaces only at a
# consumer's trust check — where it reads as "ctxloom is broken", not as
# "these two build paths disagree".
#
# THE KEY IS BAKED IN, and which one is a RULE rather than a preference: ctxloom
# and ctxloom-default are signed with the CTXLOOM publishing identity, personal
# content with the personal one.
#
# Leaving it to `git config user.signingkey` is what let this drift — that
# lookup answers from whichever repository you happen to be in, so the same
# command signs as a different identity depending on where it runs, and nobody
# finds out until a consumer's trust check fails.
SIGN_KEY := "ben+ctxloom@abbitt.me"

# The namespace every COMPANION BINARY signature is made and verified under.
#
# It is not the publish namespace, and that separation is the point: a signature
# is only valid for the domain it names, so a publish signature over a bundle
# can never be replayed as "this binary may execute on your machine". The two
# answer different questions and a key may be authorized for one and not the
# other — `allowed_signers` carries the namespaces each principal may sign in.
COMPANION_NAMESPACE := "companion.v1.ctxloom.dev"

# The key binaries are signed WITH. A public key path is correct here:
# `ssh-keygen -Y sign` takes the public half and asks the agent to sign, so no
# private key has to be readable by the build.
SIGN_PUBKEY := env_var_or_default("CTXLOOM_SIGN_PUBKEY", home_directory() / ".ssh/ctxloom_ssh_key.pub")

# The trust root a signature is checked against.
#
# `justfile_directory()` rather than a git query, because this file is imported
# by justfile.container as well: that root is bind-mounted into a container
# where a worktree's .git points at an unmounted host path and `git rev-parse`
# cannot answer. The directory of the root justfile is the repo root on both
# paths.
COMPANION_SIGNERS := justfile_directory() / ".ctxloom/allowed_signers"

# Sign one binary, leaving a detached `<file>.sig` beside it.
#
# Detached rather than embedded: the signature must cover the bytes that
# actually execute, and anything written INTO the binary changes those bytes
# after the fact. It is also why signing cannot be folded into the build's
# link step.
#
# This FAILS when no key is available, deliberately. An unsigned companion is
# refused at admission, so quietly installing one would trade a loud failure
# here for a mystifying "skipping" warning at every launch afterwards.
# BUILDING IS SIGNING. Every recipe that produces a binary signs it, not just
# `install` — because an unsigned binary is one ctxloom REFUSES to execute as a
# companion, so a build that skipped signing would hand you an artifact that
# looks finished and cannot run. The refusal arrives later, somewhere else, as
# "skipping" rather than as a build error.
#
# It also makes the from-source path work: someone following the build
# instructions ends up with `<binary>` and `<binary>.sig` side by side, which is
# what a manual copy has to carry.
#
# THE COST, stated because it is real: these recipes now need a signing key. A
# contributor without one cannot `just build`. That is the same trade the
# install path already makes, and the alternative — signing when a key happens
# to be present — produces artifacts whose runnability depends on who built
# them, which is worse to debug than a build that says what it needs.
# The stale signature is REMOVED first because `ssh-keygen -Y sign` has no
# force flag: with `<file>.sig` already present it stops at an interactive
# "Overwrite (y/n)?". With no terminal to answer it — CI, a hook, a detached
# build — it declines and leaves the PREVIOUS signature sitting beside newly
# built bytes, so the artifact verifies as tampered wherever it is finally
# checked. Removing first makes the write unconditional, which is what
# "building is signing" has to mean on the second build as well as the first.
sign-binary FILE KEY=SIGN_PUBKEY:
    rm -f {{ FILE }}.sig
    ssh-keygen -Y sign -n {{ COMPANION_NAMESPACE }} -f {{ KEY }} {{ FILE }}

# Verify a binary's detached signature against this project's trust root.
verify-binary FILE SIGNER=SIGN_KEY:
    ssh-keygen -Y verify -f {{ COMPANION_SIGNERS }} -I {{ SIGNER }} \
        -n {{ COMPANION_NAMESPACE }} -s {{ FILE }}.sig < {{ FILE }}

# --- release artifacts -----------------------------------------------------
#
# The two recipes below are invoked by .goreleaser.yml's per-build post hooks,
# which run with the working directory set to the repo root.

# UPX-compress a release binary in place.
#
# This exists HERE, rather than as goreleaser's own `upx:` pipe, because of
# ORDERING: a build's post hooks all run before that pipe does, so signing in a
# post hook would cover the UNCOMPRESSED bytes while the archive shipped the
# compressed ones — a signature that cannot verify against the binary the user
# actually runs. Compressing here puts the rewrite before the signature, which
# is the invariant `sign-binary` above states. Same flags as the local
# `_compress` in build/common.justfile; that one serves the uncompressed local
# install path and never feeds a signature.
release-compress-binary FILE:
    upx --best --lzma {{ FILE }}

# Sign a release binary and stage its detached signature under a target-keyed
# path the matching archive can read.
#
# The staging copy is what lets an archive carry ITS OWN signature. goreleaser
# lays built binaries out under per-target directories whose names carry
# suffixes it owns and may change (…_amd64_v1, …_arm64_v8.0); an archive
# reaching into that layout would bind this config to goreleaser's internals.
# Reading from a directory we name instead keeps the coupling ours.
release-sign-binary FILE OS ARCH NAME: (sign-binary FILE)
    mkdir -p dist/sigs/{{ OS }}_{{ ARCH }}
    cp {{ FILE }}.sig dist/sigs/{{ OS }}_{{ ARCH }}/{{ NAME }}.sig
