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
#  1. go:embed refuses to embed a SYMLINK ("cannot embed irregular file"), so
#     every embedded file must be a REAL file before `go build`.
#     materialize_embeds.sh derives that set from the module and copies just
#     those files; it takes the tags this script builds and tests with.
#  2. The symlinked .git confuses `go build`'s VCS stamping
#     ("error obtaining VCS status: exit status 128"), so the build must
#     pass -buildvcs=false.
set -eu

sh "$(dirname "$0")/materialize_embeds.sh" -tags "treesitter acceptance integration"

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
# A mutant that does not COMPILE is not a mutant the suite caught. `set -e` would
# exit nonzero here, and ooze reads nonzero as KILLED — scoring the compiler as
# if it were the acceptance suite. The marker lets the reporting layer subtract
# these; the exit status stays nonzero because survivor counts (what the ratchet
# gates on) must not move.
: "${CTXLOOM_VERSION_LDFLAG:?set by the justfile's _mutation-driver: an unstamped ctxloom refuses to start, so every scenario would fail and score as a kill}"
if ! CGO_ENABLED=1 go build -buildvcs=false -tags treesitter -ldflags "$CTXLOOM_VERSION_LDFLAG" -o ./ctxloom ./cmd/ctxloom; then
  echo "ooze-invalid-mutant: ./cmd/ctxloom did not compile"
  exit 1
fi
exec go test -tags "acceptance integration" -run TestAcceptance -count=1 ./tests/acceptance/...
