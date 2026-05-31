# 0015 — Local bare-git `file://` remote for integration tests (not a Gitea container)

**Date:** 2026-05-29.

## Status

Accepted.

## Context

The remote/forge integration tasks (git-transport E2E, bundle resolution, the MCP
review gate) need a "remote" that the clone/fetch/cache transport can pull from.
The original plan called for a `gitea/gitea` container helper in
`tests/integration/testenv` that boots a forge, seeds a `ctxloom` repo, and
returns a clone URL + token, skipping when docker is unavailable.

Two facts shaped the decision:

1. **The transport under test is go-git.** `RepoCache` (`EnsureRepo`/`EnsureRef`/
   `UpdateRepo`) and `GitCloneFetcher` clone and read via go-git, which treats a
   `file://` remote identically to `https://` — same clone, fetch, ref-resolution,
   and cache code paths. A local bare repo exercises the real transport.
2. **A headless Gitea container is a reliability liability.** It depends on the
   image being pullable (registry/network), needs first-run setup (install-lock,
   sqlite, admin user, token), and adds seconds of startup plus readiness-poll
   flakiness. ctxloom's prime directive is fault tolerance; a test that
   intermittently fails CI is worse than one that is fast and deterministic.

## Decision

Seed a local **bare git repository** under `t.TempDir()` and serve it over a
`file://` URL (`testenv.SeedGitRepo` / `GitRepo`). Integration tests that need a
git-transport remote use this instead of a container.

The GitHub/GitLab **API** error envelopes (401/404 shapes) — which a git remote
cannot produce — are covered separately by the in-process `ForgeStub` httptest
stand-in (see `forge_stub.go`), so no real forge is needed there either.

## Consequences

- No docker dependency, no network, no container lifecycle: the git-transport
  tests are fast and deterministic.
- forge URL detection only recognizes `github`/`gitlab` hosts (unknown hosts
  default to the GitHub **API** fetcher), so a `file://` remote does **not** flow
  through the high-level forge-detection / `ctxloom remote` CLI sync path. Tests
  drive the transport at the `RepoCache` / `GitCloneFetcher` level, where
  `file://` is fully supported.
- We do **not** test git-over-HTTP auth (token in clone URL, credential helper)
  or any Gitea-/forge-specific HTTP behavior.

**Revive trigger:** we need to test git-over-HTTP authentication, a self-hosted
forge's API behavior, or the full `ctxloom remote add <https-url>` → sync → apply
path against a live forge — i.e. coverage that a `file://` remote and the
`ForgeStub` together cannot provide.
