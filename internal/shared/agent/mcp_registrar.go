package agent

import "github.com/ctxloom/ctxloom/internal/shared/wire"

// MCPRegistrar is the MCP-registration facet of an agent: where its MCP
// config lives per scope and how one named server is merged into / removed
// from its on-disk format. It is deliberately separate from SettingsWriter —
// the writer reconciles the ctxloom-managed server set against whole files,
// while a registrar gives an external tool (taskloom manage) byte-level,
// single-server registration that preserves every foreign entry.
//
// Each agent module exports its implementation; consumers hold a registry of
// these and never learn per-agent paths or formats.
type MCPRegistrar interface {
	// Name is the agent identifier (e.g. "claude-code").
	Name() string
	// Present reports whether this agent appears to be in use for the given
	// scope: its config file or well-known directory exists. Auto-registration
	// only touches agents that are present; an explicit selection overrides.
	Present(dir string, global bool) bool
	// ConfigPath returns the agent's MCP config file for the given scope:
	// global (user-level, under the home dir) or project (under dir). Agents
	// without a usable scope return an error for it.
	ConfigPath(dir string, global bool) (string, error)
	// Install merges the named server into the config bytes (empty in → fresh
	// config). Idempotent; foreign keys and servers are preserved.
	Install(config []byte, name string, server wire.MCPServer) ([]byte, error)
	// Uninstall removes the named server from the config bytes. Removing a
	// server that is not present is a no-op, not an error.
	Uninstall(config []byte, name string) ([]byte, error)
	// Installed reports whether the named server is present in the config.
	Installed(config []byte, name string) (bool, error)
}
