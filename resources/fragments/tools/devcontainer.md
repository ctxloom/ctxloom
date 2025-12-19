## Context

### Development Containers

**Purpose:**

- Ensures consistent development environment
- Includes all required tools and dependencies
- Documents system-level requirements
- Enables reproducible builds

**Configuration:**

- Maintain `.devcontainer/` directory
- Define `devcontainer.json` with required extensions and settings
- Include Dockerfile or use pre-built images

**Best Practices:**

- Pin tool versions in the container
- Include all linters, formatters, and test tools
- Pre-install language runtimes and package managers
- Configure editor extensions in devcontainer.json

**Example devcontainer.json:**

```json
{
  "name": "Project Dev",
  "image": "mcr.microsoft.com/devcontainers/go:1.21",
  "features": {
    "ghcr.io/devcontainers/features/just": {}
  },
  "customizations": {
    "vscode": {
      "extensions": ["golang.go"]
    }
  }
}
```

