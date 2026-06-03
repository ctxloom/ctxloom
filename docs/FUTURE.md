# FUTURE

Deferred capabilities with enough design intent to be worth recording, but not
yet built. Not a commitment; a place to keep the rationale so we don't relitigate
it from scratch.

## Forge support

ctxloom ships **two forge adapters**:

- **`github`** — a rich adapter over the GitHub REST API: targeted file/dir
  reads, ref resolution, repo search, and pull-request publishing. Covers
  github.com and GitHub Enterprise (any host, via the forge's `base_url`).
- **`git`** — a generic adapter that clones over HTTPS/SSH. It works against
  **any** git host, so every forge below is already reachable for *consumption*
  (clone, read files, resolve refs). What the generic adapter does not provide is
  API-level repo search and PR/MR publishing.

So a forge is "missing" only in the sense that it lacks a native rich adapter; it
is not unreachable. Adding a native adapter buys efficient API search and
publishing for that platform.

### Candidates for a native adapter (roughly by value)

| Forge | Notes |
|---|---|
| **GitLab** (gitlab.com + self-managed) | Previously had a native adapter; removed to keep the surface to `github` + `git`. Reachable now via `git`. First candidate to restore if MR publishing/search is wanted. |
| **Gitea / Forgejo** (incl. Codeberg) | Self-hosting + OSS audience. REST API is largely GitHub-shaped, so a native adapter is high-coverage, low-effort. Best effort-to-value ratio for a third adapter. |
| **Bitbucket** (Cloud + Data Center/Server) | Two distinct REST dialects; app-password/OAuth auth. Real adapter work, no GitHub-shape shortcut. |
| **Azure DevOps Repos** | PAT auth but an org/project/repo hierarchy that does not fit the `owner/repo` model cleanly; the port would need to tolerate that addressing. |
| **SourceHut, Gerrit** | Non-PR models (email patches / change-refs). Publishing would not map onto the PR abstraction without rework. |
| **Gogs** | Gitea's ancestor; covered by the Gitea adapter if/when added. |

Intentionally skipped: **AWS CodeCommit** (AWS stopped onboarding new customers
in 2024).

### Design guardrails for adding adapters

- Keep the forge `type` open-ended, not a closed enum, so new kinds slot in
  behind the `Fetcher` port.
- Enterprise/self-hosted variants of an existing kind are configuration
  (`base_url`/`api_url`), not new adapters — GitHub Enterprise is `type: github`
  at a different host.
- The watch-outs that break "just add a `type`" are different identity/auth/repo
  models (Bitbucket auth, Azure hierarchy, Gerrit/SourceHut non-PR publishing).
  Handle those at the port boundary, not in shared code.
