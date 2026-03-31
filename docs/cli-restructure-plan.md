# CLI Restructure Plan

## Goal
Reduce cognitive load by consolidating 14+ top-level commands into 7 primary commands with consistent verb usage and max 2 levels of nesting.

## Final Structure

```
WORKFLOW (4 commands)
  run                           Run AI with assembled context
  install [ref]                 Install bundles/profiles from remotes
  update [ref]                  Check/apply updates
  edit <ref>                    Edit content

MANAGEMENT (3 commands)
  bundle
    list, show, create, delete, push, distill, export, import

  profile
    list, show, create, delete, push, export, import

  remote
    list, add, rm, default, search, browse

SETUP
  init                          Initialize .ctxloom directory

ADVANCED
  lock                          Manage lockfile
  sync                          Sync dependencies
  config                        Low-level config (mcp, plugins)

HIDDEN
  mcp, completion, hook, version
```

## Implementation Phases

### Phase 1: Restructure `remote` command
- [x] Remove `bundles` and `profiles` subcommand groups
- [x] Add `search` subcommand (merge bundles/profiles search + discover)
- [x] Add `browse` subcommand (merge bundles/profiles browse)
- [x] Keep: list, add, rm, default
- [x] Remove: pull (use `install`), publish (use `bundle/profile push`), lock, install, outdated, update, vendor, replace

### Phase 2: Restructure `bundle` command
- [ ] Add `push` subcommand (from top-level push + remote bundles publish)
- [ ] Add `delete` subcommand (new)
- [ ] Keep: list, show, create, distill, export, import
- [ ] Remove: edit (use top-level `edit`), view (merge into show), fragment/prompt/mcp subcommands (use `edit`)

### Phase 3: Restructure `profile` command
- [ ] Add `push` subcommand
- [ ] Rename `add` → `create`
- [ ] Rename `remove` → `delete`
- [ ] Remove `update` (use top-level `edit`)
- [ ] Keep: list, show, export, import

### Phase 4: Simplify `install` command
- [ ] Remove `bundle` and `profile` subcommands
- [ ] Auto-detect type from reference or local path existence
- [ ] Keep lockfile install as default (no args)

### Phase 5: Update `edit` command
- [ ] Support `edit bundle <name>` for metadata editing
- [ ] Support `edit profile <name>` for profile editing
- [ ] Keep `edit <bundle>#fragments/<name>` syntax
- [ ] Keep `edit <bundle>#prompts/<name>` syntax

### Phase 6: Move MCP management to `config`
- [ ] Create `config` command
- [ ] Add `config mcp list/add/rm/show/auto-register`
- [ ] Add `config plugin list/default/extract`
- [ ] Update `mcp` to only run server (hide from main help)

### Phase 7: Promote advanced commands
- [ ] Move `remote lock` → `lock`
- [ ] Keep/create `sync` as top-level
- [ ] Remove top-level `push` (now `bundle push`/`profile push`)
- [ ] Remove top-level `update` subcommands if any

### Phase 8: Update help text and documentation
- [ ] Update root command help
- [ ] Update all subcommand help text
- [ ] Hide: mcp, completion, hook, version from main help

### Phase 9: Add aliases for backwards compatibility (optional)
- [ ] `remote bundles pull` → warning + redirect to `install`
- [ ] `profile add` → alias for `profile create`
- [ ] `profile remove` → alias for `profile delete`

## Files to Modify

### Remove
- cmd/remote_pull.go (merge into install)
- cmd/remote_publish.go (merge into bundle/profile push)
- cmd/remote_browse.go (rewrite as remote subcommand)
- cmd/remote_search.go (rewrite as remote subcommand)
- cmd/remote_lock.go (promote to top-level lock)
- cmd/remote_vendor.go (merge into lock or remove)
- cmd/remote_replace.go (move to config or remove)
- cmd/remote_update.go (merge into update)
- cmd/push.go (merge into bundle/profile)

### Modify
- cmd/root.go (update help text)
- cmd/remote.go (simplify, add search/browse)
- cmd/bundle.go (add push, delete; remove fragment/prompt/mcp subcommands)
- cmd/profile.go (add push, rename add→create, remove→delete)
- cmd/install.go (remove bundle/profile subcommands, auto-detect)
- cmd/edit.go (extend to handle bundle/profile metadata)
- cmd/mcp.go (remove management subcommands, just serve)
- cmd/update.go (absorb remote update/outdated logic)

### Create
- cmd/config.go (new command for mcp/plugin management)
- cmd/lock.go (promote from remote)
- cmd/sync.go (if not exists)

## Testing Strategy
1. Ensure all current functionality is preserved
2. Test each new command path
3. Verify help text is clear
4. Test auto-detection in install
