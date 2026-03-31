---
title: "Troubleshooting"
---

# Troubleshooting

Solutions to common issues with ctxloom.

## Installation Issues

### Command Not Found

**Problem:** `ctxloom: command not found`

**Solutions:**

1. Ensure Go bin is in PATH:
```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

2. Reinstall:
```bash
go install github.com/ctxloom/ctxloom@latest
```

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
go install github.com/ctxloom/ctxloom@latest
```

## Context Not Injected

### Hooks Not Applied

**Problem:** Context doesn't appear in Claude Code sessions

**Check hooks configuration:**
```bash
cat .claude/settings.json | jq '.hooks'
```

**Expected output:**
```json
{
  "SessionStart": {
    "hooks": [
      {
        "type": "command",
        "command": "ctxloom hook inject-context <hash>"
      }
    ]
  }
}
```

**Fix:**
```bash
ctxloom hooks apply
```

### Context File Missing

**Problem:** Hook runs but no context appears

**Check context file:**
```bash
ls -la .ctxloom/context/
```

**Regenerate:**
```bash
ctxloom hooks apply --regenerate
```

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
# GitHub
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx

# GitLab
export GITLAB_TOKEN=glpat-xxxxxxxxxxxx
```

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
ctxloom remote add myremote owner/repo
```

### Sync Failures

**Problem:** `ctxloom remote sync` fails

**Debug:**
```bash
# Check what's being synced
ctxloom remote sync --dry-run

# Sync with verbose output
SCM_VERBOSE=1 ctxloom remote sync

# Force re-sync
ctxloom remote sync --force
```

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
ctxloom fragment list
```

**Install missing bundles:**
```bash
ctxloom fragment install remote/missing-bundle
```

## Fragment Issues

### Fragment Not Found

**Problem:** `fragment not found: myfrag`

**List available fragments:**
```bash
ctxloom fragment list
ctxloom fragment list --bundle mybundle
```

**Check fragment reference format:**
```bash
# Correct formats:
ctxloom fragment show mybundle#fragments/fragname
ctxloom fragment show fragname  # searches all bundles
```

### YAML Parse Error

**Problem:** `failed to parse bundle: yaml error`

**Validate YAML syntax:**
```bash
# Check for syntax errors
ctxloom validate .ctxloom/bundles/mybundle.yaml

# Or use a YAML linter
yamllint .ctxloom/bundles/mybundle.yaml
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
ctxloom fragment distill mybundle
```

2. **Use fewer fragments:**
```bash
# Check what's included
ctxloom run --dry-run --print | wc -c
```

3. **Create focused profiles:**
```yaml
# Instead of one large profile, create task-specific ones
# api-dev.yaml - only API-related fragments
# testing.yaml - only testing fragments
```

## MCP Server Issues

### Server Won't Start

**Problem:** `ctxloom mcp serve` fails

**Check for port conflicts:**
```bash
# MCP uses stdio, but check for other issues
ctxloom mcp serve 2>&1 | head -20
```

**Verbose mode:**
```bash
SCM_VERBOSE=1 ctxloom mcp serve
```

### Tools Not Appearing

**Problem:** MCP tools don't show up in Claude Code

**Check MCP configuration:**
```bash
cat .claude/settings.json | jq '.mcpServers'
```

**Ensure ctxloom is registered:**
```bash
ctxloom mcp auto-register --enable
ctxloom hooks apply
```

**Restart Claude Code** after configuration changes.

### Tool Execution Fails

**Problem:** MCP tool returns error

**Check ctxloom logs:**
```bash
# Run MCP server with verbose logging
SCM_VERBOSE=1 ctxloom mcp serve
```

**Test tool directly:**
```bash
# Test the underlying CLI command
ctxloom fragment list
ctxloom profile list
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

**Problem:** Remote sync/browse is slow

**Solutions:**

1. **Check network** - ensure good connectivity to GitHub/GitLab
2. **Use caching** - ctxloom caches remote content locally
3. **Reduce scope** - sync specific profiles instead of all:
```bash
ctxloom remote sync --profiles myprofile
```

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
ctxloom validate .ctxloom/config.yaml
```

### Environment Variables Not Working

**Problem:** Environment variables aren't being used

**Check variable is set:**
```bash
echo $MY_VARIABLE
```

**Check variable syntax in templates:**
```yaml
# Correct
content: "API key: {{API_KEY}}"

# Wrong
content: "API key: $API_KEY"
```

## Getting Help

### Debug Mode

Enable verbose output for any command:
```bash
SCM_VERBOSE=1 ctxloom <command>
```

### Check Version

Ensure you're on the latest version:
```bash
ctxloom --version

# Update
go install github.com/ctxloom/ctxloom@latest
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
