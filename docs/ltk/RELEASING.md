# Releasing llm-tool-killer

Releases are **version-driven and merge-triggered**, following the ctxloom
process. The version lives in the `VERSION` file and is set deliberately in each
PR; merging to `main` tags that version and publishes the release. There is **no
automatic version bumping**.

## TL;DR

```sh
# on a feature branch, with your work committed:
versionator set 0.7.0                       # pick the next version
git commit -aqm "chore: bump to v0.7.0"     # commit -a: VERSION is hand-edit-blocked by ltk
# open a PR and merge it once CI is green — that's it.
```

Merging the PR tags `v0.7.0` and triggers the build + publish. You do **not**
run `git tag` yourself.

## How it works

1. **Bump in the PR.** Use [versionator](https://github.com/benjaminabbitt/versionator)
   to set the next version — never hand-edit `VERSION` or run `git tag` (ltk's
   own rules block both):
   ```sh
   versionator set 0.7.0       # minor: 0.7.0, patch: 0.6.1, etc.
   ```
   The `VERSION` file holds the bare version (`0.7.0`); the released tag is
   `v`-prefixed (`v0.7.0`). Commit the change in your PR.

2. **Version guard (CI, on the PR).** The `version-guard` job in
   [`ci.yml`](../.github/workflows/ci.yml) fails if a tag for the `VERSION` value
   already exists. This forces every PR to a fresh version and catches "forgot to
   bump" before merge. Because every merge releases, **every PR must bump
   `VERSION`** — including docs-only changes.

3. **Merge to `main`.** Open a PR and merge once CI is green. Merging is the only
   release trigger.

4. **Tag + release (automatic).** On a successful CI run for the merge,
   [`auto-release.yml`](../.github/workflows/auto-release.yml) tags the `VERSION`
   value (`v<version>`) and pushes the tag with a PAT. That tag push triggers
   [`release-completer.yml`](../.github/workflows/release-completer.yml), which
   runs GoReleaser to:
   - build pure-Go static binaries for linux & darwin (amd64+arm64) and windows
     (amd64), with `checksums.txt`,
   - publish a GitHub Release with the archives (prerelease tags auto-flagged),
   - publish a Homebrew **cask** to
     [`ctxloom/homebrew-tap`](https://github.com/ctxloom/homebrew-tap) (`ltk`).

   If `auto-release` finds the tag already exists, it **fails** rather than
   silently skipping — a signal that `VERSION` wasn't bumped.

## Why merge-triggered, version-in-PR

- **The released SHA is always on `main`.** The tag points at the merge commit,
  so every release is reproducible from the default branch.
- **CI validates the exact merged state** before the release is cut.
- **The human chooses the version** (minor vs patch vs major) in review — no
  surprise auto-increments, and CI never commits back to `main`.

## Prerequisites (one-time)

These repository secrets must exist on `ctxloom/llm-tool-killer`:

| Secret | Purpose | Scope |
| --- | --- | --- |
| `RELEASE_TAG_TOKEN` | `auto-release.yml` pushes the release tag with it. **Must not** be the default `GITHUB_TOKEN` — tags pushed by `GITHUB_TOKEN` do not trigger `release-completer`. | PAT, `contents: write` on `ctxloom/llm-tool-killer`. |
| `HOMEBREW_TAP_GITHUB_TOKEN` | GoReleaser pushes the cask to the tap repo. | Fine-grained PAT, `contents: write` on `ctxloom/homebrew-tap` only. |

## Guardrails

- **Agents do not cut releases.** ltk dogfoods rules that block the agent from
  hand-editing `VERSION` and from running `git tag`; this exists because an agent
  once mis-released. The agent prepares the bump and PR; the tag is created by CI
  on merge, never by hand mid-session.
- **Never push a release tag with `GITHUB_TOKEN`** — it won't fire the release.
- **Never tag an unmerged branch** — the tag must be on `main`.

## Dry-running a release

GoReleaser's `prerelease: auto` means a `-rc`/`-beta` version publishes a GitHub
Release **without** touching the Homebrew tap (`skip_upload: auto`). To exercise
the pipeline before a real release, set a prerelease version in a PR
(`versionator set 0.7.0-rc.1`) and merge it; verify the run and the GitHub
Release, then delete the prerelease and its tag:

```sh
gh run watch  --repo ctxloom/llm-tool-killer <run-id>
gh release view v0.7.0-rc.1 --repo ctxloom/llm-tool-killer   # prerelease + 5 archives + checksums
gh release delete v0.7.0-rc.1 --repo ctxloom/llm-tool-killer --yes --cleanup-tag
```

## Manual release (fallback)

If you must release from your machine (note this bypasses the merge gate):

```sh
export GITHUB_TOKEN=...              # contents:write on ctxloom/llm-tool-killer
export HOMEBREW_TAP_GITHUB_TOKEN=... # contents:write on ctxloom/homebrew-tap
goreleaser release --clean
```

`release-completer.yml` also accepts a manual `workflow_dispatch` with a `tag`
input to re-cut a failed release for an existing tag.

## Troubleshooting

- **Read the real logs:** `gh run view --repo ctxloom/llm-tool-killer <run-id> --log-failed`.
- **Nothing happened after merge:** check the merge's CI run went green
  (`auto-release` only fires on a successful CI run for a push to `main`), and
  that `RELEASE_TAG_TOKEN` is set — without it the tag is never pushed.
- **Homebrew step fails / cask not updated:** confirm `HOMEBREW_TAP_GITHUB_TOKEN`
  still has `contents:write` on `ctxloom/homebrew-tap`. This step is the first
  thing a *real* (non-prerelease) release exercises that a dry-run does not.
- **`version-guard` fails on your PR:** the `VERSION` value is already tagged —
  bump it (`versionator set <next>`).

## Post-release

- Verify the GitHub Release and its assets.
- `brew install ctxloom/tap/ltk` (macOS).
- `go install github.com/ctxloom/llm-tool-killer/cmd/ltk@<tag>`.
