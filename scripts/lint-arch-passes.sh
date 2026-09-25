#!/usr/bin/env bash
# The architectural analyzer passes, run over bin/archlint. The ONE list of
# passes: `just lint-arch` (and so CI) and the lefthook pre-commit gate both
# run this script, so the two cannot drift apart.
#
# Tags reach the package loader through GOFLAGS — the driver's own -tags flag
# is accepted and then ignored. The first pass carries every tag whose files
# nothing else type-checks (treesitter needs cgo, so it rides only this host
# pass); the second runs the default build to reach the files those tags hide
# (`!schemagen`, `!docsgen`), with allowlist liveness off, because an exemption
# naming a file a pass cannot see is not stale. The GOOS passes reach files
# whose build constraints exclude the host OS; a file no pass builds is a file
# no rule checks.
set -euo pipefail

tags=mutation,arch,conformance,docker_integration,acceptance,integration,schemagen,docsgen,coveragegate
GOFLAGS=-tags=$tags,treesitter ./bin/archlint ./...
ARCHLINT_CHECK_ALLOWLISTS=0 ./bin/archlint ./...
for goos in windows darwin freebsd openbsd; do
    GOOS=$goos GOFLAGS=-tags=$tags ./bin/archlint ./...
done
