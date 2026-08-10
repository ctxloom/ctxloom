---
title: "Ad-Hoc Context Assembly"
---

Build context on the fly without creating profile files. Perfect for one-off tasks, experimentation, and quick context changes.

## The Basics

Instead of creating a profile YAML file, use command-line flags to assemble context dynamically:

```bash
# Using fragments
ctxloom run -f error-handling -f testing-patterns "help me write tests"

# Using tags
ctxloom run -t golang -t testing "help me write tests"

# Combining both
ctxloom run -f input-validation -t best-practices "review this code"
```

`-f` names a **fragment**, never a bundle. A bundle name passed to `-f` matches
nothing, and if none of the requested fragments resolve the run aborts with
`no fragments loaded`. To pull in a whole bundle's worth of context, reference
it from a profile (`-p`) or select its fragments by tag (`-t`).

## Building Faux Profiles

### Scenario: Quick Go Development Session

Instead of creating `profiles/go-dev.yaml`:

```bash
# Ad-hoc "profile" with multiple fragments
ctxloom run \
  -f project-layout \
  -f testing-patterns \
  -f error-handling \
  "implement the user service"
```

### Scenario: Security Review

Instead of creating `profiles/security-review.yaml`:

```bash
# Ad-hoc security context, by fragment name
ctxloom run \
  -f owasp-top-10 \
  -f auth-patterns \
  -f input-validation \
  -t security \
  "review this authentication code"
```

### Scenario: Multi-Language Project

```bash
# Frontend work
ctxloom run -f typescript-style -f react-patterns -f css-patterns "build the dashboard component"

# Backend work
ctxloom run -f go-style -f postgres-queries -f api-design "implement the API endpoint"

# Full-stack context
ctxloom run \
  -f typescript-style \
  -f go-style \
  -f api-design \
  "design the data flow between frontend and backend"
```

## Fragment Reference Syntax

### By Name (searches all bundles)

```bash
ctxloom run -f error-handling "help with errors"
```

The bare name is searched across every installed bundle, local and remote. This
is the form to reach for; it needs no knowledge of where the fragment lives. If
the name is ambiguous across bundles, ctxloom warns and names the alternatives
so you can qualify it.

### Fully Qualified

Qualify a fragment when two bundles define the same name, or when you want the
reference pinned to one bundle:

```bash
ctxloom run -f my-project#fragments/error-handling "help with errors"
```

A bare bundle token in front of `#fragments/` means a **local** bundle — one you
authored in `.ctxloom/content/bundles/`. Bundles installed from a remote are keyed
by their canonical URL, so a short name will not find them. Use the full form:

```bash
ctxloom run \
  -f 'https://github.com/ctxloom/ctxloom-default@bundles/security#fragments/owasp-top-10' \
  "review this authentication code"
```

`ctxloom fragment list` groups its output by exactly this key, so you can copy
the canonical bundle reference straight out of it.

### Multiple Fragments from Same Bundle

```bash
ctxloom run \
  -f my-project#fragments/testing \
  -f my-project#fragments/error-handling \
  -f my-project#fragments/concurrency \
  "review this code"
```

### Fragments from Remote Bundles

Remote fragments are served from the copy pinned in your lockfile, so the bundle
must be pulled before you can reference it:

```bash
ctxloom remote pull

# Then reference it by bare fragment name...
ctxloom run -f owasp-top-10 -f tdd "help me write secure tests"

# ...or by its canonical bundle URL
ctxloom run \
  -f 'https://github.com/ctxloom/ctxloom-default@bundles/security#fragments/owasp-top-10' \
  "help me write secure tests"
```

There is no way to reach into a remote bundle you have not pulled.

## Tag-Based Assembly

Tags let you pull in all fragments with matching tags:

```bash
# All fragments tagged "security"
ctxloom run -t security "review for vulnerabilities"

# Multiple tags (OR logic - matches any)
ctxloom run -t security -t authentication "review auth code"

# Combine with specific fragments
ctxloom run -t testing -f mocking "write unit tests"
```

## Combining with Profiles

Start with a profile, add more context:

```bash
# Base profile + extra fragments for this task
ctxloom run -p developer -f caching-strategies -f query-optimization "optimize this endpoint"

# Profile + tags
ctxloom run -p go-dev -t database -t caching "implement data layer"
```

## Preview Before Running

Always preview complex ad-hoc assemblies:

```bash
# See what would be assembled, without launching the engine
ctxloom run \
  -f project-layout \
  -f testing-patterns \
  -f input-validation \
  --dry-run
```

`--dry-run` prints the assembled context and stops. (`--one-shot` is a different
thing: it runs the engine non-interactively and prints its response. Passing
both is pointless — `--dry-run` returns before the engine is ever launched.)

## Real-World Examples

### Code Review with Specific Focus

```bash
# Performance-focused review
ctxloom run \
  -f profiling \
  -f query-optimization \
  -f concurrency \
  "review this code for performance issues"

# Security-focused review
ctxloom run \
  -f owasp-top-10 \
  -f injection \
  -f auth-patterns \
  "security review this authentication handler"
```

### Learning a New Topic

```bash
# Pull in comprehensive learning context
ctxloom run \
  -t kubernetes \
  -t containers \
  -f k8s-patterns \
  "explain how to set up a deployment"
```

### Debugging Session

```bash
# Context for debugging
ctxloom run \
  -f debugging \
  -f profiling \
  -f logging-patterns \
  "help me debug this memory leak"
```

### Writing Documentation

```bash
# Documentation-focused context
ctxloom run \
  -f api-docs \
  -f readme-patterns \
  -t documentation \
  "write API documentation for this service"
```

### Database Work

```bash
# Database context
ctxloom run \
  -f postgres-queries \
  -f query-optimization \
  -f migrations \
  "optimize this slow query"
```

## Tips for Ad-Hoc Assembly

### 1. List Available Fragments First

Fragment names are what `-f` takes, so start here — it is also how you learn the
canonical reference of every installed bundle:

```bash
# See what's available, grouped by bundle
ctxloom fragment list

# Filter by bundle
ctxloom fragment list --bundle my-project

# Search by name
ctxloom fragment list | grep security
```

### 2. Check Fragment Content

```bash
# Preview a fragment before using
ctxloom fragment show error-handling
```

### 3. Use Shell Aliases for Common Combinations

```bash
# In your .bashrc/.zshrc
alias ctxloom-go='ctxloom run -f go-style -f testing-patterns'
alias ctxloom-security='ctxloom run -t security'
alias ctxloom-review='ctxloom run -f code-review-checklist -f best-practices'

# Then use:
ctxloom-go "implement the handler"
ctxloom-security "review this code"
```

### 4. Create Shell Functions for Complex Assemblies

```bash
# In your .bashrc/.zshrc
ctxloom-fullstack() {
  ctxloom run \
    -f typescript-style \
    -f react-patterns \
    -f go-style \
    -f postgres-queries \
    -f api-design \
    "$@"
}

# Use:
ctxloom-fullstack "design the user registration flow"
```

### 5. Save Successful Combinations as Profiles

If you find yourself using the same ad-hoc combination repeatedly:

```bash
# This works well - save it!
ctxloom run -f go-style -f testing-patterns -f owasp-top-10 "..."

# Create a profile for future use — profiles take BUNDLES, not fragments
ctxloom profile create go-secure \
  -b my-project \
  -b 'https://github.com/ctxloom/ctxloom-default@bundles/security' \
  -d "Go development with security focus"
```

Note the change of unit: `run -f` takes fragment names, `profile create -b`
takes bundle references. A bare `-b` value means a *local* bundle and is not
checked at create time, so a bundle that came from a remote must be given as
`<remote-alias>/<bundle>` or as a full URL — otherwise the profile is stored
pointing at a local bundle that does not exist and quietly contributes nothing.

## When to Use Ad-Hoc vs Profiles

### Use Ad-Hoc When:
- One-off tasks
- Experimenting with different context combinations
- Quick additions to your base profile
- Task requires unusual fragment combination

### Use Profiles When:
- Same combination used repeatedly
- Team needs consistent context
- Complex inheritance chains
- Want to version control the configuration

## MCP Tool Equivalent

The same ad-hoc assembly works via MCP:

```json
{
  "tool": "assemble_context",
  "arguments": {
    "bundles": ["owasp-top-10", "testing-patterns", "error-handling"],
    "tags": ["best-practices"]
  }
}
```

The `bundles` key is a legacy wire name kept for compatibility; its entries are
**fragment** names, exactly as with `run -f`. Bundle names put there resolve to
nothing.

This lets AI assistants dynamically assemble context based on the current task.
