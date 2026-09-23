---
title: "Quick Start"
---

Every new session starts blank: the AI doesn't know your error-handling convention or the review checklist you settled on last sprint, so you re-explain it — or it guesses wrong. ctxloom fixes that once: write your standards down as fragments, and every session gets them automatically.

Here's what that buys you, then how to get it running in a few minutes.

## What ctxloom Does

| Capability | Description |
|------------|-------------|
| **Context Assembly** | Combine fragments into profiles, deliver to Claude Code through the engine's own context channel |
| **Slash Commands** | Commands become `/commands` in Claude Code automatically |
| **Session Memory** | Recover context after `/clear`, or pick up an earlier session |
| **Remote Pull** | Pull bundles from GitHub/GitLab, lockfile for reproducibility |
| **Token Optimization** | Distill fragments and commands with a cheap, fast LLM |

## Initialize Your Project

```bash
# Create .ctxloom directory in your project
ctxloom init

# Or create a global config at ~/.ctxloom
ctxloom init --home
```

`init` scaffolds a local default profile (`.ctxloom/profiles/default.yaml`,
inheriting the ctxloom-default baseline) and wires the trusted `ctxloom-default`
remote, then runs `ctxloom deps pull` to install what that profile depends on.
Run interactively, it walks you through one merged interview: pick an AI
engine, optionally add a personal remote, then launch your AI for an
agent-assisted setup. Useful flags: `--engine` to pre-select the engine,
`--remote` to add a personal repo as a trusted remote (repeatable), `--forge` to
bind those remotes to a specific forge, `--non-interactive` to skip all prompts,
`--skip-launch` to skip the auto-launch, and `--no-pull` to skip the dependency
pull.

## Review What the Remote Shipped

`ctxloom-default`'s bundles are signed by ctxloom's publishing key, which the
binary trusts, so after a fresh `init` nothing is pending. Content pulled from
any other remote is **withheld from the agent until you accept it**. Until then
a run that asks for it fails with `no fragments loaded: requested fragments not
found`, so review after every pull from a new remote:

```bash
# See what is waiting, without reviewing (non-interactive)
ctxloom review --list

# Walk each pending item: [t]rust, [r]eject, [s]kip; [T]/[R] for the rest of the bundle
ctxloom review
```

Accepting countersigns the item's exact bytes with your SSH key; if that content
later changes, it goes back to pending and you review the diff. Content you
authored in this project is exempt and never appears here. No SSH key yet? See
[Prerequisites → Signing and publishing](/getting-started/installation/#signing-and-publishing-needs-ssh) —
review still works with no key, recorded as an explicit unsigned decision.

## Browse Available Content

After initialization, explore what's available:

### List Fragments

```bash
# List all fragments from installed bundles
ctxloom fragment list

# Filter by bundle
ctxloom fragment list --bundle https://github.com/ctxloom/ctxloom-default@bundles/testing
```

Fragments are grouped by their **canonical bundle ref**, which for a remote
bundle is its repo URL plus its path in that repo:

```
  https://github.com/ctxloom/ctxloom-default@bundles/git:
    - git [default, git, workflow, version-control]

  https://github.com/ctxloom/ctxloom-default@bundles/testing:
    - mutation-testing [default, testing, mutation, quality]
    - tdd [default, testing, tdd, workflow]

  my-tools:
    - house-style [prose, conventions]
```

A **bare** bundle name in `--bundle` only matches a **local** bundle - one you
authored under `.ctxloom/content/bundles/v2/`. Remote bundles must be named by their
canonical ref, as above; `--bundle testing` fails with `no bundle named "testing"`
and lists the bundle refs it does know.

### View Fragment Content

```bash
# Show a specific fragment
ctxloom fragment show 'https://github.com/ctxloom/ctxloom-default@bundles/testing#fragments/tdd'

# Show the distilled (compressed) version
ctxloom fragment show 'https://github.com/ctxloom/ctxloom-default@bundles/testing#fragments/tdd' --distilled
```

### List Commands (Slash Commands)

```bash
# List all commands
ctxloom command list

# Filter by bundle (bare name = a local bundle you authored)
ctxloom command list --bundle my-tools
```

Commands group by canonical bundle ref too:

```
Commands (2):

  my-tools:
    - code-review
    - refactor
```

### View Command Content

```bash
# Show a specific command
ctxloom command show 'my-tools#commands/code-review'
```

## Run with Context

`-f` takes **fragment** names, not bundle names. A bundle name matches no
fragment, and the run fails with `no fragments loaded`. To pull in a whole
bundle's worth of context, use a tag (`-t`) or a profile (`-p`).

```bash
# Include a fragment by bare name (searched across every installed bundle)
ctxloom run -f tdd "Help me with this code"

# Combine multiple fragments
ctxloom run -f tdd -f git \
  "implement user authentication with tests"

# Name a fragment exactly, when the same bare name lives in several bundles
ctxloom run -f 'https://github.com/ctxloom/ctxloom-default@bundles/testing#fragments/tdd' \
  "add tests"

# Pull in every fragment carrying a tag - this is how you get a whole bundle
ctxloom run -t testing "implement user authentication with tests"

# Use a profile (pre-configured bundle/fragment set); init scaffolds "default"
ctxloom run -p default "review this PR"

# Preview what context would be sent, without launching the AI
ctxloom run -f tdd --dry-run
```

`--dry-run` prints the assembled context, the fragments loaded and the token
estimate, then stops. (It is not combined with `--one-shot`; `--one-shot` is the
separate "run non-interactively and print the response" flag, and `--dry-run`
returns before it is ever consulted.)

## Use Slash Commands

Commands in bundles become slash commands in Claude Code:

```yaml
# .ctxloom/content/bundles/v2/my-tools.yaml
commands:
  code-review:
    description: "Review code for issues"
    content: |
      Review this code for:
      - Security vulnerabilities
      - Performance issues
      - Best practice violations
```

Then in your AI CLI:
```
/code-review src/auth.go
```

## Discover Community Bundles

```bash
# Find ctxloom repositories. Only GitHub is searchable; GitLab repos can be
# added and pulled by URL, just not discovered.
ctxloom remote discover golang

# Add a remote
ctxloom remote create community alice/ctxloom-golang

# Browse remote content
ctxloom remote show community

# Author a local profile referencing the remote bundle, then pull it.
# A profile's -b accepts the short <remote>/<bundle> form.
ctxloom profile create go-testing -b community/go-testing
ctxloom deps pull

# Accept the newly pulled content, then run with it
ctxloom review
ctxloom run -p go-testing "help with tests"
```

A profile is the way to bring a whole remote bundle in. `<remote>/<bundle>` is a
**bundle** ref, so it works with `profile create -b`, but not with `run -f`,
which resolves fragments.

## Next Steps

- [Authoring Bundles](/getting-started/authoring) - Create your own bundles
- [Session Memory](/getting-started/memory) - Preserve context across sessions
- [Discovery](/guides/discovery) - Find community bundles
- [Profiles](/concepts/profiles) - Save common fragment combinations
