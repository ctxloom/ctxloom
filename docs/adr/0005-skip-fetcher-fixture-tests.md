# 0005 — Skip fetcher-fixture end-to-end content tests

**Date:** 2026-05-27.

## Status

Deferred.

## Context

`cmd/mcp_review_integration_test.go` exercises the wire-protocol review gate end-to-end: init-pending → blocked → acknowledge/decline → unblocked. Those tests verify the **gate** (which tool calls are blocked, which lockfile entries land where) but stop short of verifying the **content** flowing through `assemble_context` after acknowledge / decline.

Verifying content correctness — proving that after `acknowledge_bundle_review` the served bytes match the new SHA, and after `decline_bundle` they match the old SHA — requires a real (or convincingly faked) git clone cache holding two SHAs of the same bundle path. Building that fixture is non-trivial: git tree-object construction or a custom in-process `Fetcher` injected through the puller chain.

`BundleReader` itself has unit tests pinning the SHA-keyed read invariant. The gate tests pin the lockfile transitions. Jointly, the two layers imply end-to-end correctness.

## Decision

Don't build the fetcher fixture. Rely on the existing two test layers (unit-level `BundleReader` + gate-level wire-protocol) as joint evidence of correctness.

## Consequences

The "ack moves bytes" and "decline preserves bytes" assertions remain inferential, not directly tested. A regression where both layers individually pass but their composition produces wrong bytes would slip past the suite.

**Revive trigger:** a regression where the gate tests pass but `assemble_context` serves the wrong-SHA bytes — i.e., evidence that the two test layers don't jointly imply correctness in practice.
