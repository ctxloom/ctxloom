---
title: "Troubleshooting"
---

Solutions to common issues with ctxloom.

## Installation Issues

### Command Not Found

**Problem:** `ctxloom: command not found`

**Solutions:**

1. Ensure Go bin is in PATH:
```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

2. Reinstall from source:
```bash
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom
buf generate
go install ./cmd/ctxloom
```
The module root has no Go files — the main package is `./cmd/ctxloom`. See
[Build from Source](/getting-started/installation/#build-from-source) for
the full build (tree-sitter) command and prerequisites.

3. Verify installation:
```bash
which ctxloom
ctxloom --version
```

### Build Errors

**Problem:** Build fails with dependency errors

**Solution:**
```bash
go clean -modcache
go mod download
buf generate
go install ./cmd/ctxloom
```

## macOS Issues

### "Cannot be opened" or "Unverified developer"

**Problem:** macOS blocks ctxloom with messages like:
- "ctxloom cannot be opened because it is from an unidentified developer"
- "ctxloom cannot be opened because Apple cannot check it for malicious software"

This happens because ctxloom binaries are not code-signed or notarized with Apple (common for open-source CLI tools).

**Solution 1: Remove quarantine attribute (Recommended)**
```bash
xattr -d com.apple.quarantine /usr/local/bin/ctxloom
```

**Solution 2: Allow via System Settings**
1. Try to run `ctxloom` - it will be blocked
2. Open **System Settings** → **Privacy & Security**
3. Find the blocked app message and click **"Open Anyway"**
4. Confirm in the dialog

**Solution 3: Build from source**
```bash
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom
buf generate
go install ./cmd/ctxloom
```

### Shell completion not loading on macOS

**Problem:** Bash completions don't work after installation

**Solution:** Ensure bash-completion is installed:
```bash
brew install bash-completion
```

Add to `~/.bash_profile`:
```bash
[[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"
```

Then install the completion:
```bash
ctxloom completion bash > $(brew --prefix)/etc/bash_completion.d/ctxloom
```

## Context Not Injected

### Launched Outside ctxloom

**Problem:** Context doesn't appear in Claude Code sessions

ctxloom delivers context, hooks and its MCP server only to sessions it
launches. `ctxloom run` writes them into that session's own home; nothing is
written into the project. A `claude` started directly in the project gets none
of them.

**Fix:** start the session with `ctxloom run`. For a Claude Code you launch
yourself, write a profile's context, hooks and commands into a directory:
```bash
ctxloom profile materialize default --target .
```

### Nothing Selected

**Problem:** `ctxloom run` starts, but the context is missing what you expected

**Check what a run would deliver:**
```bash
ctxloom run --dry-run -p myprofile
```
`fragments` lists what is included and `delivery` lists where each surface goes.
Content from an unreviewed remote is withheld with an `awaiting review — run
'ctxloom review'` warning; accept it with `ctxloom review`. `ctxloom doctor`
checks the rest of the setup.

### Wrong Directory

**Problem:** ctxloom not finding configuration

**Check you're in the right directory:**
```bash
# Should show your .ctxloom directory
ls -la .ctxloom/

# Or initialize if missing
ctxloom init
```

## Remote Issues

### Authentication Failed

**Problem:** `401 Unauthorized` or `403 Forbidden` when accessing remotes

**Solution - Set authentication token:**
```bash
# GitHub (the github forge)
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
```

Non-GitHub hosts (GitLab, Gitea, self-hosted) go through the generic `git` forge, which uses ambient git auth — a credential helper, ssh-agent, or `~/.ssh/config` — so fix authentication the same way you would for a plain `git clone` of that host.

**Verify token:**
```bash
# Test GitHub access
curl -H "Authorization: token $GITHUB_TOKEN" https://api.github.com/user
```

### Rate Limiting

**Problem:** `403 rate limit exceeded`

**Solutions:**

1. Set authentication token (increases rate limit):
```bash
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
```

2. Wait for rate limit reset (usually 1 hour)

3. Check current rate limit:
```bash
curl -H "Authorization: token $GITHUB_TOKEN" \
  https://api.github.com/rate_limit
```

### Remote Not Found

**Problem:** `remote not found: myremote`

**Check configured remotes:**
```bash
ctxloom remote list
```

**Add the remote:**
```bash
ctxloom remote create myremote owner/repo
```

### Pull Failures

**Problem:** `ctxloom deps pull` fails

**Debug:**
```bash
# Log every GitHub API call ctxloom makes (method + URL) to stderr
CTXLOOM_DEBUG_HTTP=1 ctxloom deps pull
```
(`CTXLOOM_VERBOSE=1` doesn't affect this command — it only changes logging
for config/sync diagnostics and delegated-child launches.)

## Profile Issues

### Profile Not Found

**Problem:** `profile not found: myprofile`

**List available profiles:**
```bash
ctxloom profile list
```

**Check profile location:**
```bash
ls .ctxloom/profiles/
ls ~/.ctxloom/profiles/
```

### Circular Inheritance

**Problem:** `circular profile inheritance detected`

**Check profile parents:**
```bash
ctxloom profile show myprofile
```

**Fix:** Remove circular parent references in profile YAML.

### Missing Bundles

**Problem:** Profile references bundles that don't exist

**Check what bundles are available:**
```bash
ctxloom bundle list
```

**Pull missing bundles:** reference the remote bundle from a local profile, then
pull so ctxloom fetches it and updates the lockfile:
```bash
ctxloom profile create missing -b remote/missing-bundle
ctxloom deps pull
```

## Fragment Issues

### Fragment Not Found

**Problem:** `fragment not found: myfrag`

**List available fragments:**
```bash
ctxloom fragment list
ctxloom fragment list --bundle mybundle
```

**Check fragment reference format:** `fragment show` needs the bundle, while
`run -f` also accepts a bare fragment name and searches every installed bundle:
```bash
ctxloom fragment show mybundle#fragments/fragname
ctxloom run -f fragname --dry-run
```

### YAML Parse Error

**Problem:** `failed to parse bundle: yaml error`

**Validate YAML syntax:**
```bash
# Use a YAML linter
yamllint .ctxloom/content/bundles/v2/mybundle.yaml

# Or try loading the bundle
ctxloom fragment list --bundle mybundle
```

**Common YAML issues:**
- Incorrect indentation (use spaces, not tabs)
- Missing quotes around special characters
- Improper multiline string syntax

### Content Too Large

**Problem:** `warning: assembled context is 24KB (recommended max: 16KB)`

**Solutions:**

1. **Distill verbose fragments:**
```bash
ctxloom fragment distill mybundle#fragments/verbose-fragment   # one fragment
ctxloom bundle distill .ctxloom/content/bundles/v2/mybundle.yaml  # whole bundle
```

2. **Use fewer fragments:**
```bash
# Check what's included (--one-shot is ignored under --dry-run; --dry-run
# already prints the token estimate and the assembled context)
ctxloom run --dry-run

# Or get just the context and measure it directly:
ctxloom run --dry-run --format json | jq -r .context | wc -c
```

3. **Create focused profiles:**
```yaml
# Instead of one large profile, create task-specific ones
# api-dev.yaml - only API-related fragments
# testing.yaml - only testing fragments
```

## MCP Server Issues

ctxloom's MCP server has no command of its own to launch: the running
`ctxloom run` session serves it, and the session's engine is pointed at it.

### Tools Not Appearing

**Problem:** ctxloom's MCP tools don't show up in Claude Code

**Check you are in a `ctxloom run` session.** A Claude Code launched any other
way, including from a materialized profile, has no ctxloom MCP server.

**Check the server is registered:**
```bash
ctxloom mcp server list   # ctxloom is listed as "Served by: the running session's endpoint"
```
If it is missing, a profile's `exclude_mcp` is withholding it, or it has been
rejected (`ctxloom review`, or clear the rejection with `ctxloom bundle forget`).

### Tool Execution Fails

**Problem:** MCP tool returns error

Isolate the failure by running the underlying CLI command instead:
```bash
ctxloom fragment list
ctxloom profile list
ctxloom session list
```

## Performance Issues

### Slow Context Assembly

**Problem:** `ctxloom run` takes a long time

**Diagnose:**
```bash
time ctxloom run --dry-run
```

**Solutions:**

1. **Reduce fragment count** - use fewer, more focused fragments
2. **Use distillation** - smaller fragments load faster
3. **Check disk I/O** - ensure .ctxloom/ isn't on slow storage

### Slow Remote Operations

**Problem:** Remote pull/browse is slow

**Solutions:**

1. **Check network** - ensure good connectivity to GitHub/GitLab
2. **Use caching** - ctxloom caches remote content locally
3. **Reduce scope** - keep your local profiles focused so `ctxloom deps pull`
   fetches only the bundles/profiles you actually reference

## Configuration Issues

### Config Not Loading

**Problem:** Configuration changes not taking effect

**Check config location:**
```bash
# Project config
cat .ctxloom/config.yaml

# User config
cat ~/.ctxloom/config.yaml
```

**Validate config:**
```bash
# Use a YAML linter
yamllint .ctxloom/config.yaml

# Or test by showing config
ctxloom config show
```

### `{{VAR}}` Not Substituting

**Problem:** A fragment's `{{SOMEVAR}}` renders empty or literally.

Fragment templates never read the process environment — there is no
`$SOMEVAR` fallback. Mustache data comes entirely from the resolved
profile's `variables:` map (see [Templating](/guides/templating)). If
`SOMEVAR` isn't a key there, it renders empty (with a warning), regardless
of whether it's exported in your shell.

**Fix:** set the variable in the profile:
```yaml
variables:
  SOMEVAR: some-value
```

## Getting Help

### Debug Mode

`CTXLOOM_VERBOSE=1` is not a universal debug switch — it only affects
config/sync diagnostics and delegated-child (agent) launch stderr:
```bash
CTXLOOM_VERBOSE=1 ctxloom <command>
```
For most other commands it changes nothing. For GitHub API calls (`deps
pull`, `remote show`, `remote discover`, etc.), use `CTXLOOM_DEBUG_HTTP=1`
instead — it logs every request method and URL to stderr.

### Check Version

Ensure you're on the latest version:
```bash
ctxloom --version

# Update from source
cd ctxloom
git pull
buf generate
go install ./cmd/ctxloom
```

### Report Issues

If you've tried the above and still have issues:

1. **Gather information:**
```bash
ctxloom --version
go version
uname -a
```

2. **Create minimal reproduction**

3. **File issue:** https://github.com/ctxloom/ctxloom/issues

Include:
- ctxloom version
- Operating system
- Steps to reproduce
- Expected vs actual behavior
- Relevant configuration (sanitized)
