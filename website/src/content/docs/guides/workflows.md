---
title: "Common Workflows"
---

Practical workflows for using ctxloom effectively in your daily development.

## Getting Started Workflow

### 1. Initialize ctxloom

```bash
# In your project directory
ctxloom init

# Or initialize globally
ctxloom init --home
```

### 2. Discover and Add Bundles

```bash
# Find relevant bundles
ctxloom remote discover golang

# Add a remote
ctxloom remote create community alice/ctxloom-golang

# Browse what's available
ctxloom remote show community
```

### 3. Create a Profile

```bash
# Create a development profile that references the remote bundles
ctxloom profile create go-dev \
  -b community/go-development \
  -b community/testing-patterns \
  -d "Go development environment"

# Pull the referenced content
ctxloom remote pull
```

### 4. Review the Pulled Content

```bash
# Decide on each item the remote just delivered
ctxloom review
```

Content from a third-party remote is withheld from the engine until a human has
looked at it. Skip this and the run still launches — the fragments are simply
missing from the assembled context, with an "N item(s) awaiting review" notice
on stderr. (Content signed by a key you trust is exempt: `ctxloom-default` ships
with a trusted signer, so it needs no review. A `community` or `team` remote
does.)

### 5. Start Coding

```bash
# Run with your profile
ctxloom run -p go-dev "help with code"
```

## Daily Development Workflow

### Morning Setup

```bash
# Pull any remote updates your profiles reference
ctxloom remote pull

# Check your current profile
ctxloom profile show default
```

### During Development

Your context reaches the engine on its own, provided ctxloom's hooks are applied
to it, the engine supports hooks, and the content has passed the trust gate. When
it does not, the troubleshooting section below is where to look. For specific
tasks:

```bash
# Add security context for a security review (-f takes FRAGMENT names)
ctxloom run -f owasp-top-10 "review this authentication code"

# Use a specific profile for frontend work
ctxloom run -p frontend-dev "help with React component"

# Preview what context will be used, without launching the engine
ctxloom run --dry-run
```

### End of Day

```bash
# If you created new fragments, commit them
git add .ctxloom/
git commit -m "Update ctxloom configuration"
```

## Team Onboarding Workflow

### For Team Leads

1. **Create team bundles repository**:

```bash
mkdir team-ctxloom && cd team-ctxloom
mkdir -p ctxloom/bundles
```

2. **Add team standards**:

```yaml
# ctxloom/bundles/team-standards.yaml
version: "1.0"
description: Team coding standards
fragments:
  code-style:
    content: |
      # Team Code Style
      - Use gofmt for all Go code
      - 100 character line limit
      - Descriptive variable names
```

3. **Create team profile** (profiles ship inside a bundle's `profiles:` map):

```yaml
# ctxloom/bundles/team-standards.yaml (continued)
profiles:
  team-developer:
    description: Standard team development environment
    bundles:
      - team-standards
      - security-basics
```

4. **Publish**:

```bash
git init && git add . && git commit -m "Initial team ctxloom"
git remote add origin https://github.com/myorg/ctxloom-team.git
git push -u origin main
```

### For New Team Members

```bash
# Add team remote
ctxloom remote create team myorg/ctxloom-team

# Create profile that inherits the bundle-shipped team profile
ctxloom profile create my-dev \
  --parent 'https://github.com/myorg/ctxloom-team@bundles/team-standards#profiles/team-developer'

# Pull the referenced team content
ctxloom remote pull

# Review it — until you do, the team's fragments are withheld from the engine
ctxloom review

# Use the profile
ctxloom run -p my-dev "help with code"
```

If your team signs its bundles and everyone trusts the team's signing key
(`ctxloom trust signer create context@myorg.example --key team-publish.pub`), the review
step is unnecessary: content from a trusted signer is exempt from the gate. Trust
is anchored to the key, not to the remote's URL.

## Project-Specific Workflow

### Setting Up a New Project

```bash
cd my-project
ctxloom init

# Create project-specific profile
ctxloom profile create project \
  --parent go-dev \
  -b project-specific \
  -d "This project's development context"

# Use the profile
ctxloom run -p project "help with code"
```

A bare `-b` name means a bundle you authored locally, and it is what you want
here: `project-specific` is the bundle file created below. Bundles that came from
a remote must be named as `<remote-alias>/<bundle>` or as a full URL — a bare name
is stored as a local reference, is not checked at create time, and silently
contributes nothing.

### Project Bundle

Create a bundle specific to your project:

```yaml
# .ctxloom/content/bundles/project-specific.yaml
version: "1.0"
description: Project-specific context

fragments:
  architecture:
    content: |
      # Project Architecture

      This project uses:
      - Clean architecture with domain/usecase/infrastructure layers
      - PostgreSQL for persistence
      - Redis for caching
      - gRPC for internal services

  conventions:
    content: |
      # Project Conventions

      - All handlers in internal/handlers/
      - Domain models in internal/domain/
      - Use structured logging with zap
```

## Multi-Language Workflow

### Switching Contexts

```bash
# Create language-specific profiles (remote bundles by <remote-alias>/<bundle>)
ctxloom profile create go-work -b community/go-development -b community/go-testing
ctxloom profile create python-work -b community/python-development -b community/python-testing
ctxloom profile create frontend-work -b community/typescript -b community/react

# Use based on current task
ctxloom run -p go-work "help with Go code"
ctxloom run -p python-work "help with Python code"
```

### Per-Directory Configuration

Use different `.ctxloom/` configurations in different project directories:

```
~/projects/
├── go-api/
│   └── .ctxloom/
│       └── profiles/default.yaml  # Go-focused
├── python-ml/
│   └── .ctxloom/
│       └── profiles/default.yaml  # Python/ML-focused
└── react-app/
    └── .ctxloom/
        └── profiles/default.yaml  # Frontend-focused
```

## Security Review Workflow

### Setup

```bash
# Author a profile referencing the security bundles
ctxloom profile create security \
  -b ctxloom-default/security \
  -b ctxloom-default/owasp

# Pull the referenced content
ctxloom remote pull
```

### Conducting Reviews

The profile is how you pull in whole bundles; `-f` and `-t` narrow within what is
installed.

```bash
# Everything in the security profile
ctxloom run -p security "review this code for security issues"

# General security review, by tag
ctxloom run -t security "review this code for security issues"

# OWASP-focused review, by fragment name
ctxloom run -f owasp-top-10 "check for OWASP top 10 vulnerabilities"

# Authentication-specific
ctxloom run -f auth-patterns "review authentication implementation"
```

Do not reach for `-f security#fragments/owasp-top-10` here. A bare bundle token
in a qualified reference means a *local* bundle, and these bundles came from a
remote — they are keyed by their canonical URL. Use the bare fragment name, or
the full `https://github.com/ctxloom/ctxloom-default@bundles/security#fragments/owasp-top-10`.

## Code Review Workflow

### Preparing Context

```bash
# Create a code review profile (remote bundles by <remote-alias>/<bundle>)
ctxloom profile create reviewer \
  -b community/code-quality \
  -b community/testing-patterns \
  -b ctxloom-default/security \
  -d "Code review context"
```

### During Review

```bash
# Use review profile
ctxloom run -p reviewer "review this PR for code quality"

# Add specific concerns (fragment name)
ctxloom run -p reviewer -f query-optimization \
  "review for performance issues"
```

## Agents and Parallel Runs

Bind an engine and profiles under a named agent, then run it by name (see [Agents](/concepts/agents/)):

```bash
ctxloom agent create reviewer --engine claude-code --profiles reviewer
ctxloom run --agent reviewer "review this change"
```

Resume a previous session by its harp name:

```bash
ctxloom session list
ctxloom run --session swift-amber-falcon "pick up where we left off"
```

To fan one task out to several profiles in parallel and synthesize the outputs
into a single result, a coordinator spawns each as a child via the `agent_run`
MCP tool and reads their reports back itself (see [Agent
Delegation](/concepts/agent-delegation/)).

## CI/CD Integration Workflow

### In CI Pipeline

What CI can check on its own is that your context still *assembles*: that every
profile resolves, every bundle it names is reachable, and the fragments are
trusted enough to be exposed. `--dry-run` does exactly that and never launches an
engine, so the job needs no engine binary and no model credentials.

```yaml
# .github/workflows/ci.yml
jobs:
  context:
    steps:
      - uses: actions/checkout@v4
      - name: Setup ctxloom
        run: |
          go install github.com/ctxloom/ctxloom/cmd/ctxloom@latest
          ctxloom remote pull

      - name: Verify context assembles
        run: |
          ctxloom run -p code-reviewer --dry-run "review changes in this PR"
```

Two things make this work in a fresh checkout. The lockfile (committed, below)
gives `remote pull` the exact revisions to fetch. And the project approvals store
is what lets CI see the pulled content at all — a fresh machine has trusted
nothing, so run `ctxloom review --project` locally and commit `.ctxloom/approvals`
alongside the lockfile. Without it the profile still resolves but its remote
fragments are withheld, and the assembled context comes out empty.

Actually running the engine in CI (`ctxloom run -p code-reviewer --one-shot
"..." > review.md`) is possible, but it is a bigger lift than it looks: `run`
launches the configured engine as a child process, so the job must also install
that engine's binary and supply its credentials as secrets.

### Lockfile for Reproducibility

```bash
# Pulling updates the lockfile automatically
ctxloom remote pull

# Commit lockfile
git add .ctxloom/lock.yaml
git commit -m "Lock ctxloom dependencies"
```

In CI:

```bash
# Pull the exact versions recorded in the committed lockfile
ctxloom remote pull
```

## Troubleshooting Workflow

### When Context Isn't Working

```bash
# Check current configuration
ctxloom profile show default

# Preview assembled context
ctxloom run --dry-run

# Check nothing is being withheld pending review
ctxloom review --list

# Check hooks are applied
cat .claude/settings.json | jq '.hooks'

# Reapply hooks
ctxloom manage hooks install
```

### When Bundles Are Missing

```bash
# Check what's installed
ctxloom fragment list

# Check what's available remotely
ctxloom remote show ctxloom-default

# Pull missing dependencies
ctxloom remote pull
```

## Tips and Best Practices

### Keep Context Focused

```bash
# Instead of one huge profile
ctxloom profile create everything -b bundle1 -b bundle2 -b bundle3...

# Create task-specific profiles
ctxloom profile create api-dev -b community/go-development -b community/api-patterns
ctxloom profile create testing -b community/testing-patterns -b community/mocking
ctxloom profile create security -b ctxloom-default/security -b ctxloom-default/owasp
```

### Use Tags Effectively

```yaml
# In your bundles
fragments:
  quick-reference:
    tags: [quick, cheatsheet]
    content: ...

  detailed-guide:
    tags: [detailed, learning]
    content: ...
```

```bash
# Quick reference only
ctxloom run -t quick "remind me of the syntax"

# Detailed learning
ctxloom run -t detailed "explain this concept"
```

### Version Control Your Configuration

```bash
# Always commit ctxloom configuration
git add .ctxloom/
git commit -m "Update ctxloom configuration"
```

### Regular Maintenance

```bash
# Weekly: pull remote updates
ctxloom remote pull

# Monthly: review and clean up profiles
ctxloom profile list
ctxloom fragment list
```
