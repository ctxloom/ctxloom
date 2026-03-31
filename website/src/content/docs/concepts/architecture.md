---
title: "Architecture"
---

# Architecture

Understanding how SCM is designed and how its components work together.

## Overview

SCM (Sophisticated Context Manager) manages AI coding context through a layered architecture:

```
┌─────────────────────────────────────────────────────────────┐
│                        AI Tools                              │
│              (Claude Code, Cursor, etc.)                     │
└─────────────────────────────────────────────────────────────┘
                              │
                    MCP Protocol / Hooks
                              │
┌─────────────────────────────────────────────────────────────┐
│                      SCM Core                                │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │   Bundles   │  │  Profiles   │  │   Context Assembly  │ │
│  └─────────────┘  └─────────────┘  └─────────────────────┘ │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │   Remotes   │  │    Hooks    │  │    MCP Server       │ │
│  └─────────────┘  └─────────────┘  └─────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
                              │
                         File System
                              │
┌─────────────────────────────────────────────────────────────┐
│                     Storage Layer                            │
│  ┌───────────────┐  ┌───────────────┐  ┌─────────────────┐ │
│  │ .ctxloom/bundles/ │  │ .ctxloom/profiles/│  │ .ctxloom/context/   │ │
│  └───────────────┘  └───────────────┘  └─────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

## Core Components

### Bundles

**Purpose:** Package related fragments, prompts, and MCP server configs.

**Structure:**
```yaml
version: "1.0"
fragments:
  name:
    content: "..."
    tags: [...]
prompts:
  name:
    content: "..."
mcp:
  server-name:
    command: "..."
```

**Key behaviors:**
- Versioned for dependency management
- Support distillation for token efficiency
- Tags enable flexible selection

### Profiles

**Purpose:** Named configurations that assemble bundles and fragments.

**Structure:**
```yaml
description: "..."
parents: [profile1, profile2]
bundles: [bundle1, bundle2]
tags: [tag1, tag2]
```

**Key behaviors:**
- Inheritance through parents
- Merge bundles and tags from all ancestors
- One default profile active at a time

### Context Assembly

**Purpose:** Combine fragments from profiles into injectable context.

**Process:**
1. Load default profile
2. Resolve parent inheritance chain
3. Collect all referenced bundles
4. Gather fragments matching tags
5. Deduplicate by content hash
6. Write to context file

**Output:** Single markdown file in `.ctxloom/context/<hash>.md`

### Remotes

**Purpose:** Share bundles across teams and projects via Git repositories.

**Components:**
- **Registry:** Tracks configured remotes in `.ctxloom/remotes.yaml`
- **Fetcher:** GitHub/GitLab API clients for content retrieval
- **Discovery:** Search forges for SCM repositories

### Hooks

**Purpose:** Inject context into AI tool sessions automatically.

**Flow:**
```
Session Start
     │
     ▼
Hook Triggered
     │
     ▼
Read Context File
     │
     ▼
Output to AI Tool
     │
     ▼
Delete Context File
```

### MCP Server

**Purpose:** Expose SCM functionality to AI tools via Model Context Protocol.

**Capabilities:**
- List/get fragments, profiles, prompts
- Search content
- Manage remotes
- Assemble context
- Apply hooks

## Data Flow

### Context Injection Flow

```
1. User starts session
         │
         ▼
2. SessionStart hook fires
         │
         ▼
3. Hook runs: ctxloom hook inject-context <hash>
         │
         ▼
4. SCM reads .ctxloom/context/<hash>.md
         │
         ▼
5. Content output to stdout
         │
         ▼
6. AI tool receives context
         │
         ▼
7. Context file deleted
```

### Remote Sync Flow

```
1. ctxloom remote sync
         │
         ▼
2. Load profile dependencies
         │
         ▼
3. For each remote bundle:
   │
   ├─► Fetch from GitHub/GitLab
   │
   ├─► Validate structure
   │
   └─► Write to .ctxloom/bundles/
         │
         ▼
4. Update lockfile
         │
         ▼
5. Regenerate context
         │
         ▼
6. Apply hooks
```

## Directory Structure

### Project Level (`.ctxloom/`)

```
.ctxloom/
├── config.yaml          # Project configuration
├── bundles/             # Local and pulled bundles
│   ├── local-bundle.yaml
│   └── remote/
│       └── pulled-bundle.yaml
├── profiles/            # Profile definitions
│   └── default.yaml
├── context/             # Generated context files
│   └── <hash>.md
├── remotes.yaml         # Remote registry
└── lock.yaml            # Dependency lockfile
```

### User Level (`~/.ctxloom/`)

```
~/.ctxloom/
├── config.yaml          # User defaults
├── bundles/             # User-wide bundles
├── profiles/            # User-wide profiles
└── remotes.yaml         # User-wide remotes
```

## Configuration Hierarchy

Settings are merged from multiple sources (later overrides earlier):

1. **Built-in defaults**
2. **User config** (`~/.ctxloom/config.yaml`)
3. **Project config** (`.ctxloom/config.yaml`)
4. **Environment variables**
5. **Command-line flags**

## Integration Points

### Claude Code

- **Hooks:** `.claude/settings.json` → `hooks.SessionStart`
- **MCP:** `.claude/settings.json` → `mcpServers.ctxloom`

### Gemini

- **Hooks:** `.gemini/settings.json` → `hooks.SessionStart`
- **MCP:** `.gemini/settings.json` → `mcpServers.ctxloom`

## Extension Points

### Custom Backends

The backend system is extensible:

```go
type Backend interface {
    Name() string
    WriteSettings(config) error
    ReadSettings() (config, error)
}
```

### Custom Fetchers

Remote fetchers implement:

```go
type Fetcher interface {
    FetchFile(owner, repo, path, ref) ([]byte, error)
    ListDir(owner, repo, path, ref) ([]DirEntry, error)
    SearchRepos(query, limit) ([]RepoInfo, error)
    ValidateRepo(owner, repo) (bool, error)
}
```

## Design Principles

### Fault Tolerance

SCM prioritizes availability over strict correctness:

- Missing remotes → warning, continue
- Invalid bundles → skip, continue
- Hook failures → log, continue
- Network errors → use cached, continue

The user should always end up in their AI tool, even if some features degrade.

### Content Addressable

Context files use content-based hashing:

- Same content → same hash → same filename
- Changed content → new hash → new file
- Enables caching and deduplication

### Separation of Concerns

- **Bundles:** Content packaging
- **Profiles:** Configuration/selection
- **Remotes:** Distribution
- **Hooks:** Integration
- **MCP:** AI tool interface

Each layer has a single responsibility.

### Minimal Dependencies

SCM aims to work with minimal external dependencies:

- No database required
- File-based storage
- Standard Git hosting (no custom server)
- Works offline with cached content
