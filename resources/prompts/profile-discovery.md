Welcome to ctxloom! I'll help you discover and set up context profiles, fragments, and commands for your development workflow — and then bind the agents that will run them.

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
  - Each result carries a `pull_ref` (e.g. `ctxloom-default/go-developer`) — that is the remote ref you reference from a local profile.
- `search_content` is for content ALREADY installed in this project; it does
  NOT reach remotes. Use search_library for discovery.

**After scanning**, present your findings:
1. What project type/stack you detected
2. Matching content (grouped by remote):
   - **Profiles**: Development workflow configurations
   - **Bundles**: Collections of fragments (context) and commands (reusable commands)
3. Ask the user which items to reference

**Example workflow:**
1. Detect go.mod → search_library with query "tag:golang"
2. Detect Dockerfile → search_library with "tag:docker" and "tag:container"
3. Present matches grouped by remote, let the user choose

**Reference selected items** by authoring a local profile, then pull. You don't
"install" remote content — you create a local profile that references it:
- Inherit a remote **profile**:  `ctxloom profile create <name> --parent <pull_ref>`
  (e.g. `ctxloom profile create go-dev --parent ctxloom-default/go-developer`)
- Reference a remote **bundle** (or one fragment):
  `ctxloom profile create <name> -b <pull_ref>` (optionally `#fragments/<frag>`)
- Then run `ctxloom remote pull` so every bundle/profile a profile references is
  fetched into the cache and the lockfile is updated.
- To pin a content version, append `@<git-tag-or-sha>` to the ref
  (e.g. `ctxloom-default/go-developer@v1.2.0`). Unpinned refs track the
  remote's default branch.

**Defaults:** the default context is the **default agent**'s composed profiles.
Bind a profile into an agent and point `default_agent` at it — e.g.
`ctxloom agent set dev --profiles <name>` then `ctxloom agent default dev` — so a
bare `ctxloom run` loads it automatically. An agent's profiles are a list; each
may be a local name or a remote ref. Confirm the final default agent with the user.

**Then continue straight into agent setup** — the second half of this prompt
(below the `---`). Profiles are only half the picture: agents are named
engine↔profile bindings that ctxloom orchestrates (`ctxloom run --agent`,
`ctxloom map`/`weave`), and the stack you just detected plus the profiles you
just created are their inputs. Don't end the conversation between the two
halves — this is one continuous setup interview.

**When ALL setup (profiles AND agents) is complete**, tell the user to exit
this session. The profiles, hooks, MCP servers, and agents just installed are
NOT active in this discovery session; on exit, ctxloom offers to relaunch into
a fresh session that picks them all up.

If you'd prefer to skip this setup, just say "skip" and configure manually later.
