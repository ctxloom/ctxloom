# Messages for the crate's errors and notices. Each id is a code, which begins
# with the crate's name; the variables are its args. An id ending in -fix is
# the remedy of the error whose code it follows.

library-application-config-unparsable = failed to load config: config layer cannot be parsed: { $path }: yaml: { $cause }
library-application-config-unknown-key = failed to load config: the config breaks its schema:{ $violations }
library-application-config-unknown-key-fix = remove or rename the key in config.yaml (ctxloom config edit)
library-application-config-breaks-schema = failed to load config: the config breaks its schema:{ $violations }
library-application-config-breaks-schema-fix = give the key a value the schema accepts, or remove it (ctxloom config edit)
library-application-unknown-section =
    unknown section: { $section }

    Available: { $available }
library-application-bundle-unreadable = bundle unreadable: { $ask }: { $cause }
library-application-bundle-unreadable-fix = fix or remove the bundle file
library-application-no-bundles-dir = no bundles directory found
library-application-view-path = invalid path format: { $cause } (expected fragments, commands, mcp, skills, or profiles)
library-application-view-missing = { $kind } not found: { $name }
library-application-not-viewable = { $dir } items are not viewable: { $path }
library-application-item-missing =
    { $kind } "{ $name }": item not found

    Available { $kind }s: { $available }
library-application-operational-prompt-missing = the project's bundle "{ $bundle }" ({ $dir }) overrides ctxloom's built-in one whole, and has no prompt "{ $prompt }"
library-application-operational-prompt-missing-fix = add prompts/{ $prompt }.md to it, or rename that bundle to use ctxloom's own
library-application-bad-reference = invalid reference format: expected { $expected } (got "{ $got }")
library-application-wrong-kind = "{ $ask }" selects a { $got }/, not a { $want }/ (expected { $expected })
library-application-unsupported-format = { $command }: --format { $format } was accepted but this command does not support it yet (nothing was rendered through it)
library-application-unsupported-format-fix = rerun without --format
library-application-skill-not-found = skill not found: "{ $name }" in bundle "{ $bundle }"
library-application-skill-name-not-found = skill not found: { $name }
library-application-no-bundle-named = no bundle named "{ $name }" (bundles: { $known })
library-application-search-nothing = please provide a search query or tags — e.g. ctxloom search cache, or ctxloom search --tag golang
library-application-search-type = unknown type "{ $value }" (valid: fragment, command, skill, profile, bundle, mcp_server) — see 'ctxloom search --help'
library-application-search-remote = search --remote reads the remote registry, which this build does not read yet
library-application-search-remote-fix = search the project's own content with --local
library-application-agent-not-found = agent "{ $name }" not found
library-application-launch-no-agent = launch: the named agent is not found: "{ $name }"
library-application-launch-no-agent-fix = create it with `ctxloom agent create <name> --profiles <profile>` (`ctxloom profile list` names the profiles)
library-application-launch-no-project = launch: the named agent is not found: no project here, so no default agent
library-application-launch-no-project-fix = run `ctxloom init` here to set up a project and its default agent
library-application-launch-no-default = launch: the named agent is not found: no default agent is set
library-application-launch-no-default-fix = choose one with `ctxloom agent default <name>` (`ctxloom agent list` names them)
library-application-launch-no-agents = launch: the named agent is not found: no agents are configured
library-application-launch-no-agents-fix = create it with `ctxloom agent create <name> --profiles <profile>` (`ctxloom profile list` names the profiles)
library-application-mcp-not-found = MCP server "{ $name }" not found
library-application-unknown-engine = unknown engine "{ $engine }"; ctxloom drives: { $known }
library-application-engine-auth = engines.{ $engine }.auth: { $why }
library-application-compose = assemble context: profile { $profile }: { $cause }
library-application-no-fragments = no fragments loaded: { $detail }
library-application-agent-exclusive = if any flags in the group [agent { $other }] are set none of the others can be; [agent { $other }] were all set
library-application-saved-command = failed to load command: command not found: { $name }
library-application-workspace = invalid workspace "{ $value }": workspace is one of none or worktree
library-application-run-needs-dry-run = run launches the engine for a session, which this build does not do yet: run it with --one-shot for one turn, or with --dry-run to see what a launch would carry
library-application-startup-aborted = aborting startup: { $count } fatal finding(s); fix them, or rerun with --degraded (env CTXLOOM_DEGRADED=1) to launch anyway:{ $findings }

# The remedy is the fix's; the message names it so that it is an arg of both.
library-application-notice-finding =
    { $text }{ $remedy ->
       *[other] { "" }
    }
library-application-notice-finding-fix = { $remedy }
library-application-notice-remotes-unreadable = the dependencies installed from remotes cannot be read: { $cause }
library-application-notice-remotes-unreadable-fix = fix or delete the lockfile, then run `ctxloom deps pull`
library-application-notice-bundle-skipped = skipping bundle { $manifest }: bundles: reading the tree at { $dir }: { $cause }
library-application-notice-bundle-skipped-fix = fix or remove the bundle file
library-application-config-newer = { $path }: ctxloom config: schema_version { $found }: written by a newer format than this binary reads; upgrade the binary (this binary reads up to { $current })
library-application-config-newer-fix = upgrade ctxloom: { $path } was written by a newer one
library-application-config-companions-bare = { $path }: ctxloom config: schema_version { $found }: companions { $names } are registered by name alone, as an older format wrote them; a companion is registered with the command that runs it from schema_version 9
library-application-config-companions-bare-fix = register each again with `ctxloom companion add <name> --command <program> [args...]`, then remove the `companions:` list from { $path } (ctxloom does not convert it: a name alone says nothing of what to run)
library-application-config-older = { $path }: ctxloom config: schema_version { $found }: older than the oldest format this binary can migrate (the oldest is { $oldest })
library-application-config-older-fix = back up { $path }, then re-run `ctxloom init` to scaffold a current one and re-apply your settings
library-application-config-version-unreadable = { $path }: ctxloom config: format version cannot be read: schema_version is not an integer
library-application-config-version-unreadable-fix = back up { $path }, then re-run `ctxloom init` to scaffold a current one and re-apply your settings
library-application-companion-not-on-path = companion "{ $name }" is registered but its binary is not on PATH, so it contributes nothing
library-application-companion-not-on-path-fix = install it (then `ctxloom companion add { $name }` checks it), or unregister it: ctxloom companion remove { $name }
library-application-loadout-probe-failed = companion "{ $name }": loadout probe failed ({ $detail }), so its hooks, MCP servers and context are unknown and it contributes nothing this time
library-application-loadout-probe-failed-fix = make the companion's `loadout --format yaml` answer (repair or reinstall it), then re-apply
library-application-loadout-unparseable = companion "{ $name }": unparseable loadout, withholding: { $cause }
library-application-loadout-unparseable-fix = fix the loadout the companion prints for `loadout --format yaml`, or unregister it: ctxloom companion remove { $name }
library-application-notice-config-keyless = config file { $path } defines no keys
library-application-notice-runtime-weakens = runtime { $runtime } replaces { $key } { $replaced }: weakens isolation
library-application-notice-runtime-strengthens = runtime { $runtime } replaces { $key } { $replaced }: strengthens isolation
library-application-notice-runtime-ownership-swap = runtime { $runtime } replaces { $key } { $replaced }: ownership differs, not interchangeable
library-application-notice-default-engine-block = agent { $agent } names no engine: its settings were written to agents.{ $agent }.{ $block }, the default engine's block
library-application-notice-default-engine-block-fix = to write another engine's block, name it with --engine
library-application-notice-user-settings-left-out = { $path } is not a JSON object, so the { $engine } session started without these settings of yours
library-application-notice-user-settings-left-out-fix = make { $path } a JSON object, or move it aside
library-application-notice-workspace-unavailable = no worktree could be had, so the engine works in the project itself: { $reason }
library-application-notice-session-home-host = { $engine } runs in your own home (engines.{ $engine }.session_home: host): it reads and changes your own memory, MCP servers and history, and a session home is a relocation, not confinement
library-application-notice-session-home-host-fix = to give each run a session home of its own, remove session_home: host from engines.{ $engine } in your home config
library-application-notice-checkout-left = the session's checkout { $path } (branch { $branch }) was left in place: { $reason }
library-application-notice-checkout-left-fix = commit or discard its work; the session's next run re-enters it, and `ctxloom session remove` takes it once it is clean
library-application-stale-distilled = the distilled form of the { $kind } { $reference } is stale: it does not match its source as it is now, or records no digest of it, so the original was used
library-application-stale-distilled-fix = run `ctxloom { $kind } distill { $reference }` to distill it again
library-application-notice-root-ignored = CTXLOOM_ROOT={ $root } is not a directory; ignoring it

## Writes (S2). A refused write has written nothing.

library-application-write-not-wired = this build was composed without the write side's ports
library-application-write-no-project = no .ctxloom directory configured: there is no project here to write into
library-application-write-no-project-fix = run `ctxloom config create` here to make one
library-application-write-bundle-exists = bundle already exists: { $path }
library-application-write-builtin-read-only = bundle "{ $bundle }" is built into ctxloom ({ $origin }) and is read-only; a project bundle of its name, in { $dir }, overrides it
library-application-write-builtin-read-only-fix = copy it into the project and change that copy: ctxloom bundle export { $bundle } { $dir }
library-application-write-bad-bundle-name = invalid bundle name: path traversal not allowed
library-application-write-item-exists = { $kind } already exists: { $name }
library-application-write-bad-item-name = { $kind } name "{ $name }" must be a single path segment
library-application-write-bad-id = { $kind } name "{ $name }" is not an ID: one to 63 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen (no uppercase, underscore, dot, space or "/"; a bundle is not nested)
library-application-write-bad-id-fix = choose a name such as "house-rules": lowercase it, and write each underscore, dot, space or "/" as a hyphen
library-application-write-item-not-found = { $kind } not found: { $name }
library-application-write-empty-edit = refusing to overwrite { $kind } "{ $name }" with an empty buffer — the editor returned no content (crash, truncated write, or a wrapper that never saved); the item is unchanged
library-application-write-export-needs-dest = either an output file or a destination directory must be specified
library-application-write-export-exists = { $path } already exists (pass --force to overwrite)
library-application-write-import-exists = { $what } already exists: { $path } (use --force to overwrite)
library-application-write-not-a-bundle = { $path } is not a bundle: it holds no bundle.yaml
library-application-write-import-unreadable = { $path } is not a bundle this build reads: { $cause }
library-application-write-profile-exists = profile "{ $name }" already exists
library-application-write-profile-empty = at least one parent (--parent) or included bundle (--include) is required
library-application-write-parent-not-found = parent profile "{ $name }" not found
library-application-write-bad-profile-name = profile "{ $name }": a bundle profile is addressed as <bundle>#profiles/<name>
library-application-write-profile-name-segment = invalid profile name "{ $name }": a profile name is a single path segment; rename it, e.g. "a-b"
library-application-write-profile-invalid = the profile does not read after the change: { $cause }
library-application-write-agent-missing = no agent named "{ $name }" — create it with `ctxloom agent create { $name }`
library-application-write-agent-exists = agent "{ $name }" already exists — change it with `ctxloom agent edit { $name }`
library-application-write-agent-not-defined = agent "{ $name }" is not defined
library-application-write-agent-not-in-config = agent "{ $name }" not found in config.yaml
library-application-write-unknown-engine = agent "{ $name }": unknown engine "{ $engine }"; valid engines: { $valid }
library-application-write-unknown-runtime = agent "{ $name }": unknown runtime axis "{ $value }" (known: host|container-rootless|container-rootful)
library-application-write-agent-without-engine = agent "{ $name }": an agent needs an engine or at least one profile to bind an engine; neither was given
library-application-write-bad-env = agent "{ $name }": { $detail }
library-application-write-surface-refused = agent "{ $name }": { $detail }
library-application-write-surface-needs-engine = agent "{ $name }": a surface preference needs a known engine — set --engine in the same command, since which approaches exist is the engine's answer, not ctxloom's
library-application-write-root-refused = agent "{ $name }": { $detail }
library-application-write-root-needs-engine = agent "{ $name }": a root selection needs a known engine — set --engine in the same command, since which roots an approach offers is the engine's answer, not ctxloom's
library-application-write-companion-name = invalid companion name: "{ $name }" (a companion is registered under a plain name, e.g. `ctxloom companion add acme --command acme-companion`)
library-application-write-companion-program-missing = companion program not found: { $program } (for companion "{ $name }")
library-application-write-companion-no-command = companion "{ $name }" has no command: give the program after --command
library-application-write-not-a-companion = command does not answer the companion loadout probe: `{ $command } loadout --format yaml`: { $detail }
library-application-write-companion-silent = command does not answer the companion loadout probe: `{ $command } loadout --format yaml` printed no loadout
library-application-write-companion-not-registered = companion not registered: "{ $name }" (see `ctxloom companion list`)
library-application-write-mcp-server-not-found = MCP server not found: { $name }
library-application-write-mcp-invalid = mcp "{ $name }": { $detail }
library-application-write-mcp-edit-empty = aborted: the edited MCP config is empty; bundle "{ $bundle }" was not changed
library-application-write-mcp-edit-invalid = { $detail }
library-application-write-mcp-reference = invalid reference format: expected <bundle>#mcp/<name> (got "{ $got }")
library-application-write-skill-exists = skill "{ $name }": item already exists
library-application-write-skill-not-found = skill "{ $name }": item not found
library-application-write-bad-skill-name = skill name "{ $name }" must be a single path segment
library-application-write-skill-invalid = skill "{ $name }": { $detail }
library-application-write-skill-reference = invalid reference format: expected <bundle>#skills/<name> (got "{ $got }")
library-application-write-config-exists = config already exists: { $path }
library-application-write-config-missing = no config at { $path } — run 'ctxloom config create' first
library-application-write-config-engine = unknown engine "{ $engine }"; valid engines: { $valid }
library-application-write-config-corrupt = refusing to write over { $path }: it does not parse as YAML, and saving would replace it with a truncated file — fix or move it first: { $cause }
library-application-write-config-breaks = the change would leave the config breaking its schema, so nothing was written:{ $violations }
library-application-write-mcp-bare = ctxloom mcp: there is no stdio MCP server to point a client at — ctxloom's own MCP server is served by the running session's endpoint, and no ctxloom command speaks the protocol
library-application-write-mcp-bare-fix = list the servers a project registers with `ctxloom mcp server list`
library-application-write-mcp-bare-json = ctxloom mcp does not answer in json; list the servers a project registers with `ctxloom mcp server list`
library-application-write-loadout-format = unknown format "{ $format }" (supported: yaml)
library-application-write-flag-pair = { $flag }: expected NAME=value, got "{ $got }"

