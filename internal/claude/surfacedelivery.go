package claude

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// fileTemplateDelivery is claude's file-template delivery strategy for the
// CWD-BOUND surfaces — MCP (.mcp.json), commands (.claude/commands/), and settings
// (.claude/settings.json). Each surface must be materialized where the engine
// already looks (a project-rooted config file), so this strategy holds a cwd
// Placement and writes into place.Dir(). Every DeliverX delegates to claude's
// EXISTING writer targeted at that directory and returns a Delivered whose
// Cleanup reverts exactly what that call wrote — ctxloom-owned entries removed,
// user-authored entries preserved on the marker-merged surfaces.
//
// It implements agent.MCPDelivery, agent.CommandsDelivery, and
// agent.SettingsDelivery; per the delivery-seam design the Delivered handles it
// returns stay Cleanup-only. Additive only: wiring into Setup/buildArgs is a
// later slice.
type fileTemplateDelivery struct {
	place agent.Placement
	fs    afero.Fs
	// selfContainedCommands, when true, makes DeliverCommands skip the
	// GlobalCommandsDir()/WithHomeCommandsDir dedup so every command lands in
	// the target regardless of what happens to exist in the delivering
	// machine's ~/.claude/commands. Only commandsSurface.Deliver ever sets this
	// (from agent.SurfaceInputs.SelfContainedCommands, materialize's opt-out)
	// — it is irrelevant to DeliverMCP/DeliverSettings and left false
	// everywhere else.
	selfContainedCommands bool
	// mcpCommandOverride, when non-empty, replaces agent.CtxloomCommand() as
	// the ctxloom-managed .mcp.json entry's command (see
	// agent.ResolveMCPCommand). Only mcpSurface.Deliver/DeliverIsolated set
	// this (from SurfaceInputs.MCPCommandOverride) — irrelevant to
	// DeliverCommands/DeliverSettings and left "" everywhere else.
	mcpCommandOverride string
	// denyTools, when non-empty, is unioned into the settings surface's
	// permissions.deny (see writeSettingsFile / mergeDenyTools). Only
	// settingsSurface.Deliver/DeliverIsolated set this (from
	// SurfaceInputs.DenyTools) — irrelevant to DeliverMCP/DeliverCommands and
	// left nil everywhere else. Kept as a receiver field (not a
	// DeliverSettings parameter) so agent.SettingsDelivery's signature stays
	// untouched — the same technique mcpCommandOverride uses for DeliverMCP.
	denyTools []string
}

// newFileTemplateDelivery constructs the file-template strategy writing into
// place. A nil fs defaults to the OS filesystem (agent.GetFS), matching claude's
// settings/context writers so delivery and cleanup share one fs mechanism.
func newFileTemplateDelivery(place agent.Placement, fs afero.Fs) *fileTemplateDelivery {
	return &fileTemplateDelivery{place: place, fs: agent.GetFS(fs)}
}

// DeliverMCP materializes the MCP surface by delegating to writeMCPConfig
// targeted at place.Dir(): it merges ctxloom's own server, the profile+builtin
// bundle servers, and the unified/backend servers into .mcp.json, preserving any
// user-authored servers. Cleanup reverts via removeMCPConfig, which strips the
// ctxloom-marked servers back out while leaving user servers in place. Reads
// d.mcpCommandOverride (dire-five) — DeliverSettings below is deliberately NOT
// touched the same way: the override is an MCP-only concern (it replaces the
// ctxloom stdio command), and settings.json carries no such command.
// reprise:accept-drift
func (d *fileTemplateDelivery) DeliverMCP(bundle map[string]wire.MCPServer) (agent.Delivered, error) {
	dir := d.place.Dir()
	w := &ClaudeCodeHookWriter{FS: d.fs, mcpCommandOverride: d.mcpCommandOverride}
	if err := w.writeMCPConfig(dir, bundle); err != nil {
		return nil, err
	}
	return agent.DeliveredFunc(func() error { return w.removeMCPConfig(dir) }), nil
}

// DeliverCommands materializes the commands surface by delegating to
// WriteCommandFiles targeted at place.Dir(): it writes the enabled command
// exports into .claude/commands/ under a ctxloom manifest, sharing the
// directory with the user's own commands. Cleanup reverts via the same
// manifest-scoped writer with no exports (WriteCommandFiles(dir, nil, …)),
// which removes exactly the manifest-tracked set and the manifest, leaving
// user commands untouched.
//
// Claude Code loads ~/.claude/commands alongside place.Dir()'s project scope, so
// a project copy byte-identical to the global one would otherwise surface as a
// duplicate slash-command. GlobalCommandsDir resolves that same user-global dir
// (claude.go), and WriteCommandFiles forwards it to WriteManagedCommandFiles as
// WithDedupHomeDir to dedup against it. When place.Dir() IS the home directory
// itself (a global-scope delivery), the resolved home commands dir equals the
// target commands dir exactly, and WriteManagedCommandFiles's own
// dir==dedupHomeDir check disables the dedup — a directory never dedups against
// itself — so no extra guard is needed here. If the home dir can't be resolved
// (no $HOME), dedup is simply left off, matching "empty disables the dedup".
//
// When d.selfContainedCommands is set (materialize's opt-out), the
// GlobalCommandsDir()/WithHomeCommandsDir step is skipped entirely: the target
// is a PORTABLE artifact whose launch environment is not this host, so deduping
// against the delivering machine's home would silently drop commands that only
// happen to already exist here.
func (d *fileTemplateDelivery) DeliverCommands(commands []agent.CommandExport) (agent.Delivered, error) {
	dir := d.place.Dir()
	fs := d.fs
	opts := []agent.CommandFileOption{agent.WithCommandFS(fs)}
	if !d.selfContainedCommands {
		if home, err := GlobalCommandsDir(); err == nil && home != "" {
			opts = append(opts, agent.WithHomeCommandsDir(home))
		}
	}
	if err := WriteCommandFiles(dir, commands, opts...); err != nil {
		return nil, err
	}
	return agent.DeliveredFunc(func() error {
		return WriteCommandFiles(dir, nil, opts...)
	}), nil
}

// DeliverSettings materializes the settings surface (hooks + statusline +
// deny_tools) by delegating to writeSettingsFile targeted at place.Dir(): it
// replaces ctxloom-managed hooks, (re)configures the managed statusline, and
// unions d.denyTools into permissions.deny in .claude/settings.json,
// preserving user-authored entries. manageStatusline maps to the writer's
// statusLineDisabled inverse — when false the managed statusline is cleared
// rather than set. Cleanup reverts via removeSettingsFile, which strips the
// ctxloom hooks and managed statusline while leaving user entries in place —
// including permissions.deny, which is deliberately NEVER retracted (see
// mergeDenyTools's doc: a denial can only be safely added, never
// auto-removed). This surface never touches .mcp.json (that is DeliverMCP's
// surface).
func (d *fileTemplateDelivery) DeliverSettings(hooks *wire.HooksConfig, manageStatusline bool) (agent.Delivered, error) {
	dir := d.place.Dir()
	w := &ClaudeCodeHookWriter{FS: d.fs, statusLineDisabled: !manageStatusline}
	if err := w.writeSettingsFile(hooks, d.denyTools, dir); err != nil {
		return nil, err
	}
	return agent.DeliveredFunc(func() error { return w.removeSettingsFile(dir) }), nil
}

var (
	_ agent.MCPDelivery      = (*fileTemplateDelivery)(nil)
	_ agent.CommandsDelivery = (*fileTemplateDelivery)(nil)
	_ agent.SettingsDelivery = (*fileTemplateDelivery)(nil)
)
