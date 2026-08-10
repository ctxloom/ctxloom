---
title: "Discovering Remote Repositories"
---

ctxloom can search GitHub to find repositories containing bundles you can use.

## Quick Start

```bash
# Find all public ctxloom repositories
ctxloom remote discover

# Search with a keyword
ctxloom remote discover golang

# Filter by minimum stars
ctxloom remote discover --stars 10
```

## How Discovery Works

ctxloom searches for repositories named `ctxloom` or starting with `ctxloom-` on GitHub. It validates that discovered repositories have the proper `ctxloom/` structure before showing them.

### Search Sources

```bash
# Search all sources (default; only GitHub is currently searchable)
ctxloom remote discover

# GitHub explicitly
ctxloom remote discover --source github
```

### Filtering Results

```bash
# Keyword search (matches description and topics)
ctxloom remote discover python

# Minimum star count
ctxloom remote discover --stars 5

# Limit results per source
ctxloom remote discover --limit 10

# Combine filters
ctxloom remote discover security --source github --stars 10 --limit 20
```

## Command Reference

```bash
ctxloom remote discover [query] [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--source` | `-s` | `all` | Search source: `github` or `all` (only GitHub is searchable) |
| `--stars` | | `0` | Minimum star count filter |
| `--limit` | `-n` | `30` | Maximum results |

## Interactive Workflow

When you run `ctxloom remote discover`, the results are displayed in a table:

```
Searching repositories... found 5

  # │ Forge  │ Repository          │ Stars │ Description
────┼────────┼─────────────────────┼───────┼─────────────────────────────────────
  1 │ GitHub │ alice/ctxloom-golang    │   142 │ Go development context bundles
  2 │ GitHub │ corp/ctxloom-security   │    89 │ Security-focused prompts and...
  3 │ GitHub │ team/ctxloom-internal   │    34 │ Internal development standards
  4 │ GitHub │ bob/ctxloom-python      │    28 │ Python tooling fragments
  5 │ GitHub │ dev/ctxloom-testing     │    15 │ Testing patterns and practices

Add remote? Enter number ('q' or Enter to quit): 
```

Enter a number to add that repository as a remote:

```
Add remote? Enter number ('q' or Enter to quit): 1
Name for remote [alice]: golang-bundles
Added remote 'golang-bundles' → https://github.com/alice/ctxloom-golang
```

## After Adding a Remote

Once you've added a remote, you can:

### Browse Its Contents

```bash
ctxloom remote show golang-bundles
```

### Reference Remote Content

You don't install remote items into your project. Instead, author a local
profile that references the remote content, then pull what it references.

```bash
# Consume a remote bundle by referencing it from a new profile
ctxloom profile create go-dev -b golang-bundles/go-testing

# Or consume a single fragment of a bundle
ctxloom profile create go-dev -b golang-bundles/testing#fragments/table-driven

# Inherit a profile shipped inside a remote bundle (parents take a local
# profile name or the bundle-qualified canonical URL)
ctxloom profile create go-dev \
  --parent 'https://github.com/alice/ctxloom-golang@bundles/dev-bundle#profiles/go-developer'

# Fetch everything your profiles reference and update the lockfile
ctxloom deps pull

# Let a human see what came in before the agent can
ctxloom review
```

`ctxloom deps pull` only fetches — it never exposes anything. Everything
from a remote is born pending and withheld from the agent until a human
reviews it, unless you already trust the publisher's signing key. `ctxloom
review` walks the pending items and shows each one's content: `[t]rust`,
`[r]eject`, `[s]kip`, or `[T]`/`[R]` to answer for everything left in a bundle.
Trusting countersigns the exact bytes you saw with your own SSH key, so any
later change to that content — including a version upgrade — drops it back to
pending until you review it again. `ctxloom bundle trust <ref>` and `ctxloom
bundle reject <ref>` are the same two decisions as scriptable one-liners, for
scripts or CI, and `ctxloom bundle forget <ref>` clears either one — returning
the item to pending rather than answering a mistake with its opposite. Trusting a publisher's key instead (`ctxloom signer trust`)
skips this per-item review for everything they sign.

### Use Content Directly

Reference remote content for a single run without authoring a profile:

```bash
# Use a remote fragment
ctxloom run -f golang-bundles/testing#fragments/table-driven "write tests"
```

This only works once the fragment is already pulled and reviewed: `-f` never
fetches on demand, and unreviewed content isn't silently added to context —
since it's the only fragment requested here, ctxloom refuses to run with
nothing to assemble rather than send an empty prompt. Pull and review it
first (as in Reference Remote Content above) if you haven't already.

Remote profiles ship inside bundles. To run one, create a local profile that
inherits it (as above), then `ctxloom run -p go-dev`.

### Reference in Profiles

```yaml
# .ctxloom/profiles/my-profile.yaml
description: My Go development profile
parents:
  - https://github.com/alice/ctxloom-golang@bundles/dev-bundle#profiles/go-developer
bundles:
  - my-local-additions
```

## Authentication

For private repositories or to avoid rate limits, set a GitHub token:

```bash
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
```

Non-GitHub hosts (GitLab, self-hosted git) are not searchable, but you can
still add them as remotes. Any non-GitHub URL resolves to the generic git
forge, which clones with your ambient git credentials:

```bash
ctxloom remote create internal https://gitlab.example.com/team/ctxloom-internal
```

## MCP Server Integration

Repository discovery (`ctxloom remote discover`) is CLI-only. Once remotes
are added, AI assistants can search their content through the `search_library`
MCP tool, which reads the local git clones of your configured remotes and
returns a `pull_ref` for each match:

```json
{
  "tool": "search_library",
  "arguments": {
    "query": "python",
    "item_type": "bundle"
  }
}
```

## Tips

### Finding Quality Repositories

- Use `--stars` to filter for popular, well-maintained repos
- Check the description for relevance to your needs
- Browse the repo contents before referencing them

### Naming Remotes

Choose descriptive names that indicate the content type:
- `security` for security-focused bundles
- `team-standards` for your organization's standards
- `python-tools` for language-specific tooling

### Staying Updated

`ctxloom deps pull` only installs what's already pinned in the lockfile —
it never advances anything. To move a dependency's pin forward to the newest
commit its version constraint allows, use `ctxloom deps upgrade`:

```bash
# Pull exactly what's already pinned
ctxloom deps pull

# Advance pins to the newest commit each constraint allows
ctxloom deps upgrade
```

Content that changes under an upgraded pin re-gates to pending, even if
you'd already reviewed the old bytes — run `ctxloom review` again afterward
to see what changed and decide.

`ctxloom deps check [ref]` is a different, narrower command: it refreshes
the local clone and checks for available updates without applying them
(`--apply` applies; `--force` skips confirmation). Its optional argument is a
full item/bundle reference, not a remote name — a bare `golang-bundles` is
rejected; use a canonical URL, e.g. `ctxloom deps check
'https://github.com/alice/ctxloom-golang@bundles/testing#fragments/table-driven'`,
or omit the argument to check everything in the lockfile.

## Creating Discoverable Repositories

Want your bundles to be discoverable? See the [Sharing Bundles](./sharing.md) guide for how to structure and publish your own ctxloom repository.
