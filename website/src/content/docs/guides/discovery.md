---
title: "Discovering Remote Repositories"
---

# Discovering Remote Repositories

ctxloom can search GitHub and GitLab to find repositories containing bundles and profiles you can use.

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

ctxloom searches for repositories named `ctxloom` or starting with `ctxloom-` on GitHub and GitLab. It validates that discovered repositories have the proper `ctxloom/v1/` structure before showing them.

### Search Sources

```bash
# Search both GitHub and GitLab (default)
ctxloom remote discover

# GitHub only
ctxloom remote discover --source github

# GitLab only
ctxloom remote discover --source gitlab
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
| `--source` | `-s` | `all` | Search source: `github`, `gitlab`, or `all` |
| `--stars` | | `0` | Minimum star count filter |
| `--limit` | `-n` | `30` | Maximum results per source |

## Interactive Workflow

When you run `ctxloom remote discover`, the results are displayed in a table:

```
Searching repositories... found 5

  # │ Forge  │ Repository          │ Stars │ Description
────┼────────┼─────────────────────┼───────┼─────────────────────────────────────
  1 │ GitHub │ alice/ctxloom-golang    │   142 │ Go development context bundles
  2 │ GitHub │ corp/ctxloom-security   │    89 │ Security-focused prompts and...
  3 │ GitLab │ team/ctxloom-internal   │    34 │ Internal development standards
  4 │ GitHub │ bob/ctxloom-python      │    28 │ Python tooling fragments
  5 │ GitHub │ dev/ctxloom-testing     │    15 │ Testing patterns and practices

Add remote? Enter number (or 'q' to quit):
```

Enter a number to add that repository as a remote:

```
Add remote? Enter number (or 'q' to quit): 1
Name for remote [alice]: golang-bundles
Added remote 'golang-bundles' → https://github.com/alice/ctxloom-golang
```

## After Adding a Remote

Once you've added a remote, you can:

### Browse Its Contents

```bash
ctxloom remote browse golang-bundles
```

### Pull Bundles Locally

```bash
# Preview before pulling
ctxloom remote pull golang-bundles/go-testing --type bundle

# Pull without preview
ctxloom fragment install --blind golang-bundles/go-testing
```

### Use Content Directly

Reference remote content without pulling:

```bash
# Use a remote profile
ctxloom run -p golang-bundles/go-developer "help me"

# Use a remote fragment
ctxloom run -f golang-bundles/testing#fragments/table-driven "write tests"
```

### Reference in Profiles

```yaml
# .ctxloom/profiles/my-profile.yaml
description: My Go development profile
parents:
  - golang-bundles/go-developer
bundles:
  - my-local-additions
```

## Authentication

For private repositories or to avoid rate limits, set authentication tokens:

```bash
# GitHub
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx

# GitLab
export GITLAB_TOKEN=glpat-xxxxxxxxxxxx
```

## MCP Server Integration

The discovery feature is also available as an MCP tool:

```json
{
  "tool": "discover_remotes",
  "arguments": {
    "query": "python",
    "source": "github",
    "min_stars": 5
  }
}
```

This enables AI assistants to help you find and add relevant bundles during your workflow.

## Tips

### Finding Quality Repositories

- Use `--stars` to filter for popular, well-maintained repos
- Check the description for relevance to your needs
- Browse the repo contents before pulling everything

### Naming Remotes

Choose descriptive names that indicate the content type:
- `security` for security-focused bundles
- `team-standards` for your organization's standards
- `python-tools` for language-specific tooling

### Staying Updated

After adding remotes, keep them in sync:

```bash
# Sync all remote dependencies
ctxloom remote sync

# Update specific remote
ctxloom remote update golang-bundles
```

## Creating Discoverable Repositories

Want your bundles to be discoverable? See the [Sharing Bundles](./sharing.md) guide for how to structure and publish your own ctxloom repository.
