package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// MCPFileConfig is the shared reconciler for an engine whose MCP registry is
// a raw JSON file of the shape {"mcpServers": {name: server}, ...}: it writes
// ctxloom-managed stdio servers while preserving user-authored entries
// (including remote/url servers) byte-for-byte, and tracks managed ownership
// in a sidecar ledger so renames and removals in config/bundles propagate
// instead of orphaning entries. Antigravity (.agents/mcp_config.json) and
// Kiro (.kiro/settings/mcp.json) both reconcile through this one
// implementation — the engine writers supply only paths, labels, and their
// wire plugin key.
type MCPFileConfig struct {
	// FS is the engine writer's filesystem (already default-resolved).
	FS afero.Fs
	// Path is the MCP registry file; LedgerPath the managed-names sidecar.
	Path       string
	LedgerPath string
	// Label names the file in warnings/errors (e.g. ".agents/mcp_config.json").
	Label string
	// PluginKey selects this engine's entries from wire.MCPConfig.Plugins.
	PluginKey string
	// Warn is the diagnostics sink (never fails the write).
	Warn func(format string, args ...interface{})
	// CommandOverride, when non-empty, replaces CtxloomCommand() as the
	// ctxloom-managed stdio server's command (see ResolveMCPCommand) — set
	// ONLY for an isolated-container cell (the in-container ctxloom binary
	// path). Empty (the default) preserves today's host self-exec-absolute
	// behavior exactly. Read-only for WriteServers; RemoveServers/
	// ManagedPresent never consult it (removal keys off the ledger, not the
	// command value).
	CommandOverride string
}

// mcpFileServersKey is the top-level key holding the server map — the same
// for every engine this reconciler serves.
const mcpFileServersKey = "mcpServers"

// mcpFileServer is the stdio server shape ctxloom writes. Remote servers
// (url/serverUrl) are user-authored and pass through raw. Unexported
// (U101-F28): it had no users outside this file.
type mcpFileServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// mcpFile is the loaded registry: the server map plus every other top-level
// field kept raw so fields ctxloom doesn't model survive a rewrite.
type mcpFile struct {
	Servers map[string]json.RawMessage
	Other   map[string]json.RawMessage
}

// WriteServers reconciles the managed set into the registry file: every
// previously managed name (the ledger, plus the well-known ctxloom name for
// pre-ledger files) is dropped, then the current managed set — the ctxloom
// server (unless auto-register is off), bundle servers, config servers, and
// this engine's plugin servers — is re-added and the ledger rewritten.
func (c MCPFileConfig) WriteServers(mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) error {
	mf, err := c.load()
	if err != nil {
		return fmt.Errorf("failed to load existing %s: %w", c.Label, err)
	}

	c.dropManaged(mf)

	var managed []string
	add := func(name string, s mcpFileServer) {
		c.setServer(mf, name, s)
		managed = append(managed, name)
	}

	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		add(MCPServerName, mcpFileServer{Command: ResolveMCPCommand(c.CommandOverride), Args: CtxloomMCPArgs})
	}
	for name, server := range bundleMCP {
		add(name, mcpFileServer{Command: server.Command, Args: server.Args, Env: server.Env})
	}
	if mcp != nil {
		for name, server := range mcp.Servers {
			add(name, mcpFileServer{Command: server.Command, Args: server.Args, Env: server.Env})
		}
		if backendServers, ok := mcp.Plugins[c.PluginKey]; ok {
			for name, server := range backendServers {
				add(name, mcpFileServer{Command: server.Command, Args: server.Args, Env: server.Env})
			}
		}
	}

	if err := c.save(mf); err != nil {
		return err
	}
	return c.writeLedger(managed)
}

// RemoveServers drops every managed server from the registry (leaving an
// absent file absent and user entries intact) and clears the ledger.
func (c MCPFileConfig) RemoveServers() error {
	if exists, _ := afero.Exists(c.FS, c.Path); exists {
		mf, err := c.load()
		if err != nil {
			return fmt.Errorf("failed to load existing %s: %w", c.Label, err)
		}
		c.dropManaged(mf)
		if err := c.save(mf); err != nil {
			return err
		}
	}
	return c.writeLedger(nil)
}

// ManagedPresent reports whether any managed server (the well-known ctxloom
// name, or a ledger name) is present in the registry — the Status probe.
func (c MCPFileConfig) ManagedPresent() (bool, error) {
	exists, _ := afero.Exists(c.FS, c.Path)
	if !exists {
		return false, nil
	}
	mf, err := c.load()
	if err != nil {
		return false, fmt.Errorf("failed to load existing %s: %w", c.Label, err)
	}
	if _, ok := mf.Servers[MCPServerName]; ok {
		return true, nil
	}
	for _, name := range c.readLedger() {
		if _, ok := mf.Servers[name]; ok {
			return true, nil
		}
	}
	return false, nil
}

// dropManaged removes every previously managed entry: the ledger names, plus
// the well-known ctxloom server name for pre-ledger files.
func (c MCPFileConfig) dropManaged(mf *mcpFile) {
	delete(mf.Servers, MCPServerName)
	for _, name := range c.readLedger() {
		delete(mf.Servers, name)
	}
}

// setServer marshals a typed stdio server entry into the raw server map.
func (c MCPFileConfig) setServer(mf *mcpFile, name string, s mcpFileServer) {
	data, err := json.Marshal(s)
	if err != nil {
		c.Warn("failed to marshal MCP server %q: %v", name, err)
		return
	}
	mf.Servers[name] = data
}

// load reads the registry or returns an empty structure: a missing or empty
// file (some engines create zero-byte registries) and an unparsable one all
// degrade to empty with a warning — a corrupt registry must never block the
// engine from starting.
func (c MCPFileConfig) load() (*mcpFile, error) {
	mf := &mcpFile{
		Servers: make(map[string]json.RawMessage),
		Other:   make(map[string]json.RawMessage),
	}

	data, err := afero.ReadFile(c.FS, c.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return mf, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return mf, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Was: warn and degrade to an EMPTY table. WriteServers then wrote
		// that table straight back, so an unparsable registry was silently
		// REPLACED by one containing only ctxloom's managed servers — every
		// user-authored server and every foreign top-level field destroyed on
		// a success path (U101-F03). "I could not read it" is not "it was
		// empty"; refuse and say so, the same posture U045-F02/F03 already
		// established for codex's config.toml.
		return nil, fmt.Errorf("cannot parse %s (%w) — refusing to write over an MCP registry ctxloom could not read; fix or move the file and re-run", c.Label, err)
	}
	if serversRaw, ok := raw[mcpFileServersKey]; ok {
		if err := json.Unmarshal(serversRaw, &mf.Servers); err != nil {
			c.Warn("failed to parse %s in %s: %v - existing MCP servers may not be preserved", mcpFileServersKey, c.Label, err)
		}
		delete(raw, mcpFileServersKey)
	}
	mf.Other = raw
	return mf, nil
}

// save writes the registry back atomically (canonical JSON). When nothing
// remains (no servers, no other fields) and the file does not exist, nothing
// is written — uninstall never creates files.
func (c MCPFileConfig) save(mf *mcpFile) error {
	output := make(map[string]interface{})
	for k, v := range mf.Other {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			c.Warn("failed to preserve %s field %q: %v", c.Label, k, err)
			continue
		}
		output[k] = val
	}
	if len(mf.Servers) > 0 {
		servers := make(map[string]interface{}, len(mf.Servers))
		for name, rawServer := range mf.Servers {
			var val interface{}
			if err := json.Unmarshal(rawServer, &val); err != nil {
				c.Warn("failed to preserve MCP server %q: %v", name, err)
				continue
			}
			servers[name] = val
		}
		output[mcpFileServersKey] = servers
	}

	if len(output) == 0 {
		if exists, _ := afero.Exists(c.FS, c.Path); !exists {
			return nil
		}
	}
	if err := c.FS.MkdirAll(filepath.Dir(c.Path), 0755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", c.Label, err)
	}
	data, err := CanonicalJSON(output)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", c.Label, err)
	}
	return AtomicWriteFile(c.FS, c.Path, data, c.Label)
}

// readLedger returns the managed server names from the ledger, if any.
func (c MCPFileConfig) readLedger() []string {
	data, err := afero.ReadFile(c.FS, c.LedgerPath)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

// writeLedger persists the managed names, removing the ledger when empty.
// Atomic so a crash mid-write can't leave a torn ledger and silently orphan
// a managed stdio server in the registry — the exact failure the ledger
// exists to prevent.
func (c MCPFileConfig) writeLedger(names []string) error {
	if len(names) == 0 {
		if exists, _ := afero.Exists(c.FS, c.LedgerPath); exists {
			return c.FS.Remove(c.LedgerPath)
		}
		return nil
	}
	sort.Strings(names)
	return iox.WriteFileAtomicFs(c.FS, c.LedgerPath, []byte(strings.Join(names, "\n")+"\n"), 0644)
}
