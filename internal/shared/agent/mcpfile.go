package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
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
	// Path is the MCP registry file. LedgerDir is the directory holding the
	// shared managed-content marker (internal/shared/ledger) that records
	// which server names in Path are ctxloom's — normally Path's own
	// directory, but named separately because an engine may keep the registry
	// and the marker apart.
	Path      string
	LedgerDir string
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
// (url/serverUrl) are user-authored and pass through raw. Unexported: it
// had no users outside this file.
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
//
// A name derived this round that is NOT in the ledger but IS already present
// in the registry is a hand-authored entry ctxloom never wrote (the ledger
// unconditionally drops every name it lists just below, so anything still
// present afterward was never the ledger's to begin with). WriteServers
// refuses to overwrite it: that one name is skipped (warned, not claimed in
// the ledger) and every other managed name is still written — a single
// collision must not block the rest of the reconcile, and claiming the name
// anyway would compound today's silent overwrite into a silent deletion the
// next time RemoveServers or a config change drops it from the managed set.
func (c MCPFileConfig) WriteServers(mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) error {
	mf, err := c.load()
	if err != nil {
		return fmt.Errorf("failed to load existing %s: %w", c.Label, err)
	}

	ledgerNames, err := c.readLedger()
	if err != nil {
		return fmt.Errorf("failed to read ledger %s: %w", c.ledger().Path(), err)
	}
	// handDeleted must be computed against the registry as loaded, BEFORE
	// dropManaged clears every ledger name from it below — see
	// reconcileLedger's doc.
	handDeleted := c.reconcileLedger(mf, ledgerNames)

	c.dropManaged(mf, ledgerNames)

	// A later source (config, plugin) can shadow an earlier one
	// (bundle) under the same name — including MCPServerName itself. seen
	// dedupes `managed` so the ledger records each name once, not once per
	// source that wrote it.
	var managed []string
	seen := make(map[string]bool)
	collisionWarned := make(map[string]bool)
	add := func(name string, s mcpFileServer) {
		if !seen[name] {
			if _, exists := mf.Servers[name]; exists {
				if !collisionWarned[name] {
					collisionWarned[name] = true
					c.Warn("refusing to overwrite MCP server %q in %s: a hand-authored entry already uses this name and ctxloom did not create it; rename it in your config or bundle, or rename/remove the existing entry, to let ctxloom manage %q", name, c.Label, name)
				}
				return
			}
			if handDeleted[name] {
				c.Warn("recreating MCP server %q in %s: it was removed by hand since the last write, but config, a bundle, or a plugin still declares it; remove it from there instead if you want ctxloom to stop managing it", name, c.Label)
			}
		}
		c.setServer(mf, name, s)
		if !seen[name] {
			seen[name] = true
			managed = append(managed, name)
		}
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
		ledgerNames, err := c.readLedger()
		if err != nil {
			return fmt.Errorf("failed to read ledger %s: %w", c.ledger().Path(), err)
		}
		c.dropManaged(mf, ledgerNames)
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
	names, err := c.readLedger()
	if err != nil {
		return false, fmt.Errorf("failed to read ledger %s: %w", c.ledger().Path(), err)
	}
	for _, name := range names {
		if _, ok := mf.Servers[name]; ok {
			return true, nil
		}
	}
	return false, nil
}

// dropManaged removes every previously managed entry: the ledger names the
// caller already read — WriteServers and RemoveServers each read the ledger
// exactly once and pass the names in here, so a single read serves both the
// drop and, in WriteServers, the hand-deletion check reconcileLedger runs
// against that same pre-drop snapshot — plus the well-known ctxloom server
// name for pre-ledger files. Deleting an absent map key is a no-op, so this
// never fails.
func (c MCPFileConfig) dropManaged(mf *mcpFile, ledgerNames []string) {
	delete(mf.Servers, MCPServerName)
	for _, name := range ledgerNames {
		delete(mf.Servers, name)
	}
}

// reconcileLedger makes the ledger's claims checkable before dropManaged
// clears every name it lists from the registry: it reports which ledger
// names the registry no longer has, evaluated against mf as loaded (i.e.
// before dropManaged runs).
//
// That "missing" case is the one drift the reconcile cannot already handle
// silently. The other two ledger/registry drift shapes need no new code
// here:
//   - a ledger name config/bundles/plugins no longer derive this round:
//     dropManaged still drops it, and nothing re-adds it, so it is released
//     for good — the existing drop-then-recreate cycle already does this,
//     silently, because "no longer wanted" is the expected case, not a
//     surprise.
//   - a ledger name still present but hand-edited to a different
//     definition: dropManaged deletes it by NAME regardless of content, and
//     the re-add below rewrites it to ctxloom's canonical value. This is the
//     "managed" contract working as designed — every managed name has always
//     been fully owned and rewritten each pass with no diffing — and adding
//     content-drift detection here would be a second, noisier mechanism next
//     to a cycle that already reconciles it.
//
// The case this DOES catch — a name the ledger claims that the registry no
// longer contains at all — means a human deleted that entry by hand since
// the last write. If this round still derives that name, WriteServers is
// about to silently resurrect a server the user deliberately removed; unlike
// the two cases above, that is worth a warning rather than staying silent.
func (c MCPFileConfig) reconcileLedger(mf *mcpFile, ledgerNames []string) (handDeleted map[string]bool) {
	handDeleted = make(map[string]bool, len(ledgerNames))
	for _, name := range ledgerNames {
		if _, ok := mf.Servers[name]; !ok {
			handDeleted[name] = true
		}
	}
	return handDeleted
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
		// a success path. "I could not read it" is not "it was empty"; refuse
		// and say so, the same posture corrupt-config handling already
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
	// A preserved field is re-emitted as its ORIGINAL bytes; each decode below
	// is only the gate that decides whether the value can be carried through.
	// Handing the DECODED value to the canonicaliser instead would round every
	// number past float64's exact range (1234567890123456789 →
	// 1234567890123456800), rewriting a file the user authored with no warning
	// and a success exit code.
	output := make(map[string]interface{})
	for k, v := range mf.Other {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			c.Warn("failed to preserve %s field %q: %v", c.Label, k, err)
			continue
		}
		output[k] = json.RawMessage(v)
	}
	if len(mf.Servers) > 0 {
		servers := make(map[string]interface{}, len(mf.Servers))
		for name, rawServer := range mf.Servers {
			var val interface{}
			if err := json.Unmarshal(rawServer, &val); err != nil {
				c.Warn("failed to preserve MCP server %q: %v", name, err)
				continue
			}
			servers[name] = json.RawMessage(rawServer)
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

// ledger is this registry's managed-name record, scoped to the MCP surface so
// it can share one marker with any other surface writing into LedgerDir.
//
// It used to be a private read/write pair here, duplicated in three engines.
// internal/shared/ledger owns the mechanism now — see that package's doc for
// why one shared implementation, and why the surface type is open.
func (c MCPFileConfig) ledger() ledger.Ledger {
	return ledger.Ledger{FS: c.FS, Dir: c.LedgerDir, Warn: c.Warn}
}

func (c MCPFileConfig) readLedger() ([]string, error) {
	return c.ledger().Read(ledger.SurfaceMCP)
}

func (c MCPFileConfig) writeLedger(names []string) error {
	return c.ledger().Write(ledger.SurfaceMCP, names)
}
