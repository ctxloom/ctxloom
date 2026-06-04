Welcome to ctxloom! I'll help you discover and set up context profiles, fragments, and prompts for your development workflow.

**First, scan the current directory** for project indicators like:
- go.mod, Cargo.toml, package.json, pyproject.toml, requirements.txt
- Dockerfile, docker-compose.yml, Makefile, justfile
- .github/ and other CI/CD configs
- Framework-specific files (next.config.js, vite.config.ts, etc.)

**Surface (read this first):**
- The configured remotes have already been cloned locally during init. Read the
  `ctxloom://remotes` MCP resource to see them.
- Use the **search_library** MCP tool to find matching bundles/profiles across
  ALL remotes. It reads the local clones (no network).
  - Search by tag: `tag:golang`, `tag:react`, `tag:docker`
  - Search by text: `security`, `testing`, `ci-cd`
  - Optionally pass item_type ("bundle" or "profile") to narrow.
  - Each result carries a `pull_ref` (e.g. `ctxloom-default/go-developer`) — that is what you install.
- `search_content` is for content ALREADY installed in this project; it does
  NOT reach remotes. Use search_library for discovery.

**After scanning**, present your findings:
1. What project type/stack you detected
2. Matching content (grouped by remote):
   - **Profiles**: Development workflow configurations
   - **Bundles**: Collections of fragments (context) and prompts (reusable commands)
3. Ask the user which items to install

**Example workflow:**
1. Detect go.mod → search_library with query "tag:golang"
2. Detect Dockerfile → search_library with "tag:docker" and "tag:container"
3. Present matches grouped by remote, let the user choose

**Install selected items** with the CLI, then sync:
- Profile:  `ctxloom profile install <pull_ref>` (e.g. `ctxloom profile install ctxloom-default/go-developer`)
- Bundle/fragment/prompt:  `ctxloom install <pull_ref>`
- Then run `ctxloom remote sync` so every bundle a profile depends
  on is fetched into the cache.
- To pin a content version, append `@<git-tag-or-sha>` to the ref
  (e.g. `ctxloom-default/go-developer@v1.2.0`). Unpinned installs track the
  remote's default branch.

**Defaults:** the first profile you install is promoted into `defaults.profiles`
in `.ctxloom/config.yaml` so `ctxloom run` loads it automatically. To make a
different profile the default later, edit `defaults.profiles` in that file (or use
the `ctxloom profile` subcommands). Confirm the final `defaults.profiles` list
with the user before exiting.

If you'd prefer to skip this setup, just say "skip" and configure manually later.
