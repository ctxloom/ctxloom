//go:build parked_engines

package opencode

// This file is opencode's LAYOUT vocabulary: the project-relative directory
// ctxloom's own writers target, plus the vendor-owned facts describing where
// the opencode BINARY itself keeps its config/credential state under the
// user's home. Unlike claude/codex, opencode has no agent.EngineCLI
// declaration (enginecli.go) yet — these constants exist so
// internal/lm/isolation's opencodeOverlayDirs/credentialSeedSpecs/
// engineContainerSpec tables and internal/gitignore's WorktreeArtifactPatterns
// have a single owner to check their opencode-shaped literals against,
// instead of each hand-maintaining its own copy. isolation/gitignore cannot
// import this package in production (opencode -> internal/acp ->
// internal/lm/isolation, and acp -> internal/gitignore, are real cycles), so
// their copies stay literals there; tests/arch's engine-layout gate is the
// enforcement point.

// ConfigDirName is opencode's project-scoped managed-config directory,
// relative to the workspace root — the parent of the command/skill dirs
// (commandfiles.go/skillfiles.go) and the ctxloom-context.md file
// (settings.go).
const ConfigDirName = ".opencode"

// XDGConfigHomeEnv is the XDG env var opencode honors for its config home.
// MEASURED against opencode 1.18.1, not vendor-documented — see
// internal/lm/isolation/auth.go's opencode credentialSeedSpecs entry for the
// full live-verification account.
const XDGConfigHomeEnv = "XDG_CONFIG_HOME"

// XDGDataHomeEnv is the XDG env var opencode honors for its state store:
// auth.json, mcp-auth.json, and native session data, all nested one level
// under DataDirName. Same measurement basis as XDGConfigHomeEnv.
const XDGDataHomeEnv = "XDG_DATA_HOME"

// DataDirName is the app-name directory opencode appends onto whatever
// XDG_DATA_HOME resolves to (default ~/.local/share when the var is unset)
// for its state store — NOT the same thing as ConfigDirName above, which is
// ctxloom's own project-relative managed-config directory.
const DataDirName = "opencode"

// AuthFileName is opencode's credential file inside DataDirName, the file
// `opencode auth login` writes.
const AuthFileName = "auth.json"

// MCPAuthFileName is opencode's per-MCP-server OAuth token file, alongside
// AuthFileName — present only when an OAuth-authenticated MCP server has
// been configured, so its absence alone must never block seeding
// AuthFileName.
const MCPAuthFileName = "mcp-auth.json"
