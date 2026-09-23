---
title: "Sharing Bundles"
---

Share your context bundles with your team or the community by creating a ctxloom repository.

## Repository Structure

A ctxloom repository follows this structure:

```
my-ctxloom-repo/
├── .ctxloom/
│   └── content/
│       └── bundles/
│           └── v2/
│               ├── my-bundle/
│               │   ├── bundle.yaml       # the envelope: version, description, tags
│               │   ├── fragments/
│               │   │   └── testing.md
│               │   ├── prompts/          # commands
│               │   │   └── code-review.md
│               │   ├── profiles/
│               │   │   └── go-developer.yaml
│               │   ├── SHA256SUMS        # written when the bundle is signed
│               │   └── .sigs/
│               └── another-bundle/
└── README.md
```

A bundle is distributed as a directory tree, one file per item, and a remote
repository lays its bundles out exactly as a consuming project stores its own:
under `.ctxloom/content/bundles/v2/`. When you add a repository as a remote,
ctxloom checks for `.ctxloom/content/` and warns if it is missing. Remote
repositories distribute bundles only; profiles ship inside a bundle's
`profiles/` directory (see below).

`SHA256SUMS` and `.sigs/` are the publisher signature ctxloom writes when a
bundle is signed (see Sign Your Bundles below). A signature from a key your
consumer trusts is the only thing that spares them ctxloom's review step.
Everything else pulled from this repo is born pending and withheld from the
agent until a human reviews it.

## Creating a Bundle

### Bundle File Structure

The envelope, `bundle.yaml`, carries the bundle's own fields and no items:

```yaml
# .ctxloom/content/bundles/v2/go-development/bundle.yaml
version: 1.0.0
description: Go development context and best practices
author: your-name
tags:
  - golang
  - development
```

Each fragment is a Markdown file under `fragments/`. Its YAML front matter
holds the item's fields and the body is its content.

`.ctxloom/content/bundles/v2/go-development/fragments/testing.md`:

```markdown
---
tags:
  - testing
---
# Go Testing Best Practices

- Use table-driven tests
- Use testify/assert for assertions
- Name tests descriptively: TestFunction_Scenario_Expected
```

Commands have the same shape, under `prompts/`.

`.ctxloom/content/bundles/v2/go-development/prompts/code-review.md`:

```markdown
---
description: Review Go code for best practices
tags:
  - review
---
Review this Go code for:
- Error handling completeness
- Test coverage
- Idiomatic patterns
```

MCP servers (`mcp/`), hooks (`hooks/<event>/`) and profiles (`profiles/`) are
YAML files; skills (`skills/`) are directories. `ctxloom bundle create` makes a
single-file bundle, which works locally but cannot be published: `bundle push`
refuses it and asks for a directory with a `bundle.yaml`.

### Bundle Fields

| Field | Required | Description |
|-------|----------|-------------|
| `version` | No | Free-form label (e.g., `1.0`, `2.1.3`); ctxloom does not validate or enforce it, and it does not drive version pinning — pinning is by git tag/SHA/semver range in the reference (see Versioning below) |
| `description` | No | Human-readable description |
| `author` | No | Author name or organization |
| `tags` | No | Bundle-level tags (inherited by all items) |
| `fragments/` | No | One Markdown file per fragment |
| `prompts/` | No | One Markdown file per command |
| `profiles/` | No | One YAML file per profile shipped with the bundle |
| `hooks/` | No | Hooks shipped with the bundle, one YAML file per hook |
| `mcp/` | No | One YAML file per MCP server |

### Fragment Fields

| Field | Required | Description |
|-------|----------|-------------|
| `content` | No | The fragment content: the file's Markdown body. ctxloom does not require it, but a fragment with no content has nothing to give the agent |
| `tags` | No | Additional tags (merged with bundle tags) |
| `notes` | No | Human-readable notes (not sent to AI) |
| `no_distill` | No | Prevent automatic distillation |

Fields other than `content` go in the file's front matter. Commands take the same fields plus `description` and an optional `llm:` block with per-backend slash-command export settings.

## Sharing a Profile

Profiles ship inside a bundle's `profiles/` directory; a remote repository has no top-level profiles directory. Add the profile to the bundle that carries the content it composes:

```yaml
# .ctxloom/content/bundles/v2/go-development/profiles/go-developer.yaml
description: Complete Go development environment
bundles:
  - go-development
  - testing-patterns
tags:
  - golang
  - best-practices
```

Consumers inherit a bundle-shipped profile by its bundle-qualified canonical URL (parents accept a local profile name or this full form):

```bash
ctxloom profile create dev \
  --parent 'https://github.com/username/my-ctxloom-bundles@bundles/go-development#profiles/go-developer'
```

## Publishing to GitHub

### 1. Create Repository

```bash
# Create new repo
mkdir my-ctxloom-bundles
cd my-ctxloom-bundles
git init

# Create structure
mkdir -p .ctxloom/content/bundles/v2
```

### 2. Add Your Content

Create one directory per bundle in `.ctxloom/content/bundles/v2/`.

### 3. Add README

```markdown
# My ctxloom Bundles

Context bundles for [description].

## Installation

```bash
ctxloom remote create mybundles username/my-ctxloom-bundles
ctxloom profile create dev -b mybundles/go-development
ctxloom deps pull
ctxloom review
```

`ctxloom deps pull` only fetches this bundle; `ctxloom review` is what
actually lets its fragments and commands reach the agent — pulled content is
born pending and withheld until a human reviews it, unless you already trust
this bundle's publisher key.

## Available Bundles

- **go-development** - Go best practices and patterns
- **testing-patterns** - Testing strategies and examples
```

### 4. Sign Your Bundles

Signing is what spares your consumers that review step: content signed by a
key they trust is exempt from it, so it reaches their agent as soon as they
pull it. Everything else is born pending regardless of how it was published.
Signing only authenticates the bundle as genuinely yours — it never vouches
for whether it's safe — so trusting your key is a separate decision each
consumer makes for themselves (`ctxloom signer trust`).

The easiest way to sign is at publish time: `ctxloom bundle push my-bundle
mybundles --sign` (see Validation below) signs the exact bytes it publishes
and writes the signature for you. Use this if you aren't committing to this
repository by hand.

If you're pushing this repository with plain git instead (the next step),
sign first, inside a real ctxloom project (see Validation), then commit both
files yourself:

```bash
ctxloom bundle sign my-bundle
```

This writes a `SHA256SUMS` manifest and a detached signature under `.sigs/`
inside the bundle's directory in your project. Copy the whole bundle directory
into this repo's `.ctxloom/content/bundles/v2/` before the commit below;
`ctxloom bundle export my-bundle <dest-dir>` copies it whole, `SHA256SUMS` and
`.sigs/` included.

### 5. Push to GitHub

```bash
git add .
git commit -m "Initial ctxloom bundles"
git remote add origin https://github.com/username/my-ctxloom-bundles.git
git push -u origin main
```

## Making Your Repository Discoverable

### Naming Convention

Name your repository `ctxloom` or `ctxloom-*` for automatic discovery:

- `ctxloom` - General ctxloom content
- `ctxloom-golang` - Go-specific bundles
- `ctxloom-security` - Security-focused content
- `ctxloom-team-standards` - Team standards

### GitHub Topics

Add relevant topics to your repository:

- `ctxloom-bundles`
- `claude-code`
- `ai-context`
- Language-specific: `golang`, `python`, `typescript`

### Description

Write a clear description that helps users find your bundles:

> "ctxloom bundles for Go development: testing patterns, error handling, and best practices"

## Versioning

### Semantic Versioning

Use semantic versioning for bundles:

- **Major** (1.0 → 2.0): Breaking changes
- **Minor** (1.0 → 1.1): New fragments/features
- **Patch** (1.0.0 → 1.0.1): Bug fixes, typo corrections

ctxloom does not parse, validate, or enforce the bundle's `version:` field —
it's a label for your own bookkeeping and changelog. What a consumer actually
pins to is a git tag, SHA, or semver range in their reference, below.

### Git Tags

Tag releases for version pinning:

```bash
git tag v1.0.0
git push origin v1.0.0
```

Users can then pin to specific versions by referencing the tagged ref:

```bash
ctxloom profile create dev -b mybundles/go-development@v1.0.0
ctxloom deps pull
ctxloom review
```

## Best Practices

### Content Quality

1. **Be concise** - AI context has size limits
2. **Be specific** - Vague guidance isn't helpful
3. **Be actionable** - Include examples and patterns
4. **Test your content** - Use your bundles before publishing

### Organization

1. **One topic per bundle** - Don't mix unrelated content
2. **Use tags consistently** - Enable profile-based selection
3. **Document variables** - If using templates, document required variables
4. **Include examples** - Show how to use your bundles

### Maintenance

1. **Keep bundles updated** - Review and update regularly
2. **Accept contributions** - Enable issues and PRs
3. **Changelog** - Document changes between versions
4. **Deprecation** - Clearly mark deprecated content

## Team Repositories

For team/organization use:

### Private Repositories

ctxloom works with private repos when authenticated:

```bash
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
ctxloom remote create team https://github.com/myorg/ctxloom-internal
```

### Monorepo Structure

For larger organizations:

```
org-ctxloom/
├── .ctxloom/
│   └── content/
│       └── bundles/
│           └── v2/
│               ├── frontend-react/
│               ├── frontend-typescript/
│               ├── backend-go/
│               ├── backend-python/
│               ├── shared-security/
│               └── shared-testing/
└── README.md
```

Each bundle is one directory directly under `v2/`. Team profiles (frontend-dev, backend-dev, fullstack-dev) go in the `profiles/` directory of the bundle they belong with.

### Access Control

- Use GitHub/GitLab teams for access control
- Consider separate repos for different access levels
- Public bundles in public repo, sensitive standards in private

## Validation

`ctxloom fragment show` and `ctxloom run --dry-run` resolve against your
project's configured bundles (`.ctxloom/content/bundles/`, plus pinned
remotes). Author and validate inside a real ctxloom project (`ctxloom init`,
if the directory you're publishing from doesn't already have one). Write the
bundle at `.ctxloom/content/bundles/v2/my-bundle/`. That directory is
committed, it is what `ctxloom bundle sign` signs, and it is the tree
`ctxloom bundle push` reads from. Then:

```bash
# Check YAML syntax of the envelope
yamllint .ctxloom/content/bundles/v2/my-bundle/bundle.yaml

# Test loading
ctxloom fragment show my-bundle#fragments/testing

# Test in a profile
ctxloom run --dry-run -f my-bundle#fragments/testing
```

Publish with `ctxloom bundle push my-bundle mybundles` (add `--sign` to sign
it as part of the same push, or `--pr` to open a pull request instead of
pushing directly). That is the supported publish path, and it writes the
bundle to `.ctxloom/content/bundles/v2/` in the target repo.

## Example Repositories

Look at these repositories for inspiration:

- Community bundles follow the patterns described here
- Check the `ctxloom-default` default remote for examples
- Search GitHub for `ctxloom-` repositories
