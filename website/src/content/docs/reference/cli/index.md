---
title: "CLI Reference"
sidebar:
  order: 0
---

Complete reference for all ctxloom commands.

The per-command pages in this section are **generated** from the command definitions in `internal/adapters/cli` (`just gen-docs`) — the same text as `ctxloom <command> --help` and `man ctxloom`, so they always match the binary. This page keeps the narrative that doesn't fit a `--help` screen.

Every command accepts the global `--format` flag (`text`, `json`, `yaml`, `toml` or `markdown`); the structured formats emit machine-readable output for scripting and frontends.

## Commands

The [`ctxloom`](/reference/cli/ctxloom/) page lists every top-level command, generated from the binary, with a link to each.

## Workflow guidance

With no `-p`/`-f`/`-t` and no default profile configured, `ctxloom run` shows an interactive picker of installed profiles (skipped when not on a terminal).

With `--one-shot` and no prompt argument, the prompt is read from **stdin** when piped — making `run --one-shot` a universal reducer over any input (e.g. output collected from other tools or an earlier run):

```bash
# Synthesize piped input with a high-power profile:
cat findings.txt | ctxloom run -p code-review/synthesis --one-shot
```

A profile may declare its own preferred LLM (`llm:`); `run -l`/`--llm` overrides it. To fan a task across several agents in parallel, a running coordinator spawns them as children with the `agent_run` MCP tool and reads their reports back itself — see [Agent Delegation](/concepts/agent-delegation/).

## Reference grammar

Bundle-item references follow one grammar everywhere:

```
bundle#fragments/name     # Fragment in bundle
bundle#commands/name      # Command in bundle
bundle#mcp/name           # MCP config in bundle
bundle#profiles/name      # Profile shipped by bundle (local bundle short form;
                          # remote bundles use the canonical URL)
remote/bundle             # Bundle from a configured remote
remote/bundle@v1.0.0      # Versioned bundle
https://github.com/o/r@bundles/b#profiles/n   # Bundle-shipped profile (canonical)
```

`profile create -b` and `profile modify --add-bundle`/`--remove-bundle` accept the **per-remote short form** `<remote>/<bundle>[#<type>/<item>]` (e.g. `-b ctxloom-default/testing`). It is expanded to that remote's canonical URL before the profile is stored, so a stored profile never carries a machine-local alias. A bare name with no `<remote>/` prefix is a local bundle and is stored as written. Full URLs (`https://github.com/owner/repo@bundles/name`) and `ctxloom:local@...` refs pass through unchanged.

Parent refs (`--parent`, `--add-parent`) expand the same way when they name a bundle-shipped profile (`<remote>/<bundle>#profiles/<name>`). A parent with no `#profiles/` selector is always a local profile name, so subdirectory paths like `personal/go-developer` work.

**Consuming remote content is reference-only** — you don't "install" remote items. Author a local profile that references remote content, then pull:

```bash
ctxloom profile create testing -b ctxloom-default/testing
ctxloom profile create security -b ctxloom-default/security#fragments/owasp-top-10
ctxloom deps pull
```

## Two nouns: the source and the closure

[`remote`](/reference/cli/ctxloom_remote/) is **where content comes from** — a registry of repository URLs in `.ctxloom/remotes.yaml`. Registering, defaulting and removing one is local bookkeeping: no fetch, no credential, nothing installed.

[`deps`](/reference/cli/ctxloom_deps/) is **what this project has** — the lockfile, and every verb that moves it. Its verbs mirror apt: [`pull`](/reference/cli/ctxloom_deps_pull/) makes the installation match upstream (installing what is missing, removing what a remote has stopped publishing, and never advancing an existing pin), [`check`](/reference/cli/ctxloom_deps_check/) reports which pins could move, [`upgrade`](/reference/cli/ctxloom_deps_upgrade/) moves them, and [`list`](/reference/cli/ctxloom_deps_list/) reads the closure offline. Content from an untrusted remote is withheld per item until accepted with `ctxloom review`, whatever the lockfile says.

A remote that could not be READ never counts as having deleted anything: `pull` proves it can read a repository before treating any absence there as authority, so an expired credential or an outage leaves the installation exactly as it is and says which remotes went unchecked.

A reference's `@version` is a **constraint** — a semver range (`@^1.2`), a branch (`@main`), an exact tag/SHA (`@v1.2.3`), or empty (default branch). `upgrade` moves only the lockfile, never your profile YAML. Freeze an item with `ctxloom deps hold <name>`; release with `ctxloom deps unhold`. A hold is not a manifest pin: a pin is the exact version an author wrote, a hold is a freeze you applied, and the held commit still satisfies the constraint. See [Versioning, locking, and holds](/concepts/remotes/#versioning-locking-and-holds).

## Forges

A remote binds to a **forge** — the adapter ctxloom uses to read and publish. `github` is the rich adapter over the GitHub REST API (file reads, ref resolution, repo search, PR publish; serves github.com and GitHub Enterprise). `git` is the generic adapter that clones over HTTPS/SSH and reads the working copy — it works against any git host (GitLab, Gitea, Bitbucket, self-hosted) for consumption, with ambient git auth, but has no API search or PR publish.

Without `--forge`, the forge resolves from the URL host: github.com uses `github`, every other host uses `git`. Pass `--forge` to override with `github`, `git`, or a `forges:` label configured in `remotes.yaml` (e.g. a GitHub Enterprise instance with its own `base_url`/`token_env`).

| URL format | Example |
|------------|---------|
| GitHub shorthand | `alice/ctxloom` |
| Full HTTPS | `https://github.com/alice/ctxloom` |
| Any other git host | `https://git.example.com/corp/ctxloom` |
| SSH (converted to HTTPS) | `git@github.com:alice/ctxloom.git` |
