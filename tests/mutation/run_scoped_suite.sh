#!/bin/sh
# Invoked by ooze (see trust_cascade_mutation_test.go) as the mutant test
# command. Runs with cwd = a SYMLINKED-then-partially-materialized copy of
# this repo living in a tmpdir (ooze's laboratory.Test): every file is a
# symlink back to the real checkout except the one mutated source file,
# which ooze overwrites with real mutated bytes at this same relative path.
#
# Two things a bare `WithTestCommand("go build ...")` cannot do, which is
# why this exists as its own script:
#
#  1. go:embed refuses to embed a SYMLINK ("contains no embeddable files" /
#     "irregular file") — verified empirically. This repo embeds resources/,
#     container/, cmd/ltk, cmd/taskloom, internal/shared/harp,
#     internal/agentcoord/mcpschema/schemas, and internal/config's
#     allowed_signers. Every embed target must be a REAL file before `go
#     build`, so this script re-materializes (symlink -> real copy) just
#     those directories before building — a few hundred files, not the whole
#     tree, so it stays cheap (measured: well under a second).
#  2. The symlinked .git confuses `go build`'s VCS stamping
#     ("error obtaining VCS status: exit status 128"), so the build must
#     pass -buildvcs=false.
set -eu

for d in resources cmd/ltk cmd/taskloom internal/shared/harp container \
         internal/agentcoord/mcpschema/schemas internal/config; do
  [ -d "$d" ] || continue
  find "$d" -type l | while IFS= read -r f; do
    tgt=$(readlink -f "$f")
    rm -f "$f"
    cp "$tgt" "$f"
  done
done

# The build output must be a REAL file in the laboratory before `go build`
# writes it. ./ctxloom is one of the symlinks ooze mints back to the real
# checkout whenever a built binary is sitting at its root, and `go build -o`
# WRITES THROUGH an existing symlink rather than replacing it — verified
# empirically. So without this the mutant build lands its bytes on the
# developer's own ./ctxloom, and every process that resolves the binary by
# project root (tests/integration/testenv findAppBinary, which is what the
# acceptance suite runs) executes a MUTATED ctxloom for as long as the
# mutation gate is running. That is a silent cross-run corruption: a
# concurrent acceptance suite fails in whatever code the current mutant
# touched, attributing the mutation's damage to the branch under test.
rm -f ./ctxloom
CGO_ENABLED=1 go build -buildvcs=false -tags treesitter -o ./ctxloom ./cmd/ctxloom
exec go test -tags "acceptance integration" -run TestAcceptance -count=1 ./tests/acceptance/...
