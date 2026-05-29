---
description: Discover and install profiles, bundles, and fragments
---

Scan the current project and discover matching ctxloom content from configured remotes.

## Surface (read this first)

- **Listings are MCP resources, not tools.** Read `ctxloom://remotes` for the
  configured remotes, and `ctxloom://profiles` / `ctxloom://fragments` /
  `ctxloom://prompts` for what is already installed locally.
- **`search_content` searches LOCAL content only** (installed bundles/profiles
  in this project's cache). It does NOT reach remotes — do not use it to
  discover remote content.
- **Browsing a remote's catalog is CLI:** `ctxloom remote browse <remote>`.
- **Installing is CLI** (`ctxloom install` / `ctxloom profile install`), then
  the `sync_dependencies` MCP tool fetches bundle dependencies.

## Steps

1. **Scan the project directory** for indicators like:
   - go.mod, Cargo.toml, package.json, pyproject.toml, requirements.txt
   - Dockerfile, docker-compose.yml, Makefile, justfile
   - .github/, .gitlab-ci.yml, and other CI/CD configs
   - Framework-specific files (next.config.js, vite.config.ts, etc.)

2. **List configured remotes** by reading the `ctxloom://remotes` resource.

3. **Browse each remote's catalog** with the CLI:
   - `ctxloom remote browse <remote>` — lists bundles and profiles with their
     installable references.
   - `ctxloom remote browse <remote> --type bundle` / `--type profile` to filter.
   Match the listed names/descriptions against the stack you detected (e.g.
   `go-developer`, `python-development`, `typescript-development`,
   `web-frontend`, `docker`/`container` bundles).

4. **Present your findings**:
   - What project type/stack you detected
   - Matching content from each remote:
     - **Profiles**: Development workflow configurations
     - **Bundles**: Collections of fragments (context) and prompts (reusable commands)
   - Ask the user which items to install

5. **Install selected items** with the CLI, then sync dependencies:
   - `ctxloom profile install <remote>/<name>` (e.g. `ctxloom profile install ctxloom-default/go-developer`)
   - `ctxloom install <reference>` for an individual bundle/fragment/prompt
   - Call the `sync_dependencies` MCP tool afterward so every bundle a profile
     depends on is fetched into the cache.
   - To pin a specific content version, append a git tag or commit SHA to the
     reference with `@`: `ctxloom-default/go-developer@v1.2.0`. (Unpinned
     installs track the remote's default branch. Note: the `version:` field in
     `remotes.yaml` — e.g. `v1` — is the ctxloom *schema directory*, not a git
     ref; leave it alone.)
   - The first profile you install is auto-promoted into `defaults.profiles` in
     `config.yaml`. To make a *different* profile the default later, edit
     `defaults.profiles` in `.ctxloom/config.yaml` (or use `ctxloom profile`
     commands).

## Example workflow

1. Read `ctxloom://remotes` -> see `ctxloom-default` is configured
2. Detect go.mod + Dockerfile -> `ctxloom remote browse ctxloom-default`
3. Spot `go-developer` profile and `docker`/`go-ai-practices` bundles in the listing
4. Present matches grouped by remote, let the user choose
5. `ctxloom profile install ctxloom-default/go-developer`, then call `sync_dependencies`

If the user says "skip", acknowledge and let them know they can run `/discover` again later.
