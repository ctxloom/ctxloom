# Releasing ctxloom

Releases are **version-driven and merge-triggered**. The version lives in the
`VERSION` file and is set deliberately in each PR; merging to `main` tags that
version and publishes the release. There is **no automatic version bumping**.

## TL;DR

```sh
# on a feature branch, with your work committed:
versionator set v0.7.0        # pick the next version (minor/patch/major)
git add VERSION && git commit -m "chore: bump to v0.7.0"
# open a PR and merge it once CI is green — that's it.
```

Merging the PR tags `v0.7.0` and triggers the build + publish. You do **not**
run `git tag` yourself.

## How it works

1. **Bump in the PR.** Use [versionator](https://github.com/benjaminabbitt/versionator)
   to set the next version — never hand-edit `VERSION` or run `git tag`:
   ```sh
   versionator set vX.Y.Z      # e.g. v0.7.0 for a minor, v0.6.1 for a patch
   ```
   Commit the `VERSION` change in your PR.

2. **Version guard (CI, on the PR).** The `version-guard` job in `ci.yml` fails
   if a tag for the `VERSION` value already exists. This forces every PR to a
   fresh version and catches "forgot to bump" / duplicate tags before merge.
   Because every merge releases, **every PR must bump `VERSION`** — including
   docs-only changes.

3. **Merge to `main`.** Open a PR and merge once CI is green. Merging is the
   only release trigger; pushing to `main` without a release-worthy change isn't
   a thing here (the guard enforces a unique version).

4. **Tag + release (automatic).** On a successful CI run for the merge,
   `auto-release.yml` tags the `VERSION` value and pushes the tag using a PAT.
   That tag push triggers `release-completer.yml`, which runs GoReleaser to:
   - build standard (`CGO_ENABLED=0`) and full (tree-sitter, CGO) binaries,
     statically linked and UPX-compressed on linux/windows,
   - publish a GitHub release with archives + checksums + install scripts,
   - publish Homebrew **casks** to [`ctxloom/homebrew-tap`](https://github.com/ctxloom/homebrew-tap)
     (`ctxloom` and `ctxloom-full`).

If `auto-release` finds the tag already exists, it **fails** rather than
silently skipping — a signal that `VERSION` wasn't bumped.

## Why merge-triggered, version-in-PR

- **The released SHA is always on `main`.** The tag points at the merge commit,
  so every release is reproducible from the default branch.
- **CI validates the exact merged state** before the release is cut.
- **The human chooses the version** (minor vs patch vs major) in review — no
  surprise auto-increments, and CI never commits back to `main`.

## Prerequisites (one-time)

These repository secrets must exist on `ctxloom/ctxloom`:

| Secret | Purpose | Scope |
| --- | --- | --- |
| `RELEASE_TAG_TOKEN` | `auto-release.yml` pushes the release tag with it. **Must not** be the default `GITHUB_TOKEN` — tags pushed by `GITHUB_TOKEN` do not trigger `release-completer`. | PAT, `contents: write` on `ctxloom/ctxloom`. |
| `HOMEBREW_TAP_GITHUB_TOKEN` | GoReleaser pushes the casks to the tap repo. | Fine-grained PAT, `contents: write` on `ctxloom/homebrew-tap` only. |

## Guardrails

- **Agents do not cut releases.** Automated tooling (e.g. ltk) blocks the agent
  from running `git tag` / release commands; this exists because an agent once
  mis-released. The agent prepares the bump and PR; the tag is created by CI on
  merge, never by hand mid-session.
- **Never push a release tag with `GITHUB_TOKEN`** — it won't fire the release.
- **Never tag an unmerged branch** — the tag must be on `main`.

## Hotfixes / re-releasing

A given version tags exactly once. To ship a fix, bump to the next patch
(`versionator set vX.Y.(Z+1)`) in a new PR and merge. To re-cut a failed
release for an existing tag, use the `release-completer` workflow's manual
`workflow_dispatch` (note: see its `tag` input handling).
