package engine

import (
	"errors"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// DynamicApproach is the engine's provided way of consuming items served on
// the session's MCP endpoint: how the endpoint is named to the engine (the
// entry its MCP file carries). Optional on the Definition; Base.Delegate
// routes preface items to it when present.
type DynamicApproach interface {
	present.Approach
	Endpoint(ep sessions.Endpoint) wire.MCPServer
}

// BearerEntry is the one projection of the session endpoint as an MCP file
// entry: its URL with the bearer on the Authorization header. Every dynamic
// approach whose native form is a URL entry returns it rather than spelling
// the header itself.
func BearerEntry(ep sessions.Endpoint) wire.MCPServer {
	return wire.MCPServer{URL: ep.URL, Headers: map[string]string{"Authorization": "Bearer " + ep.Credential}}
}

// The per-kind approach interfaces. Each embeds present.Approach (name and
// traits) and adds the typed Deliver for its kind, so the Definition's typed
// fields cannot receive another kind's approach. Deliver writes the kind's
// inputs under the advised start, at the root the plan selected, and
// reports where they landed on both sides; it is called ONLY by the static
// delivery adapter.

// ContextApproach delivers the engine's context surface.
type ContextApproach interface {
	present.Approach
	DeliverContext(start present.Start, root present.RootKind, in ContextInputs, fs afero.Fs) (present.Delivered, error)
}

// MCPApproach delivers the engine's MCP server config.
type MCPApproach interface {
	present.Approach
	DeliverMCP(start present.Start, root present.RootKind, in MCPInputs, fs afero.Fs) (present.Delivered, error)
}

// SettingsApproach delivers the engine's settings surface.
type SettingsApproach interface {
	present.Approach
	DeliverSettings(start present.Start, root present.RootKind, in SettingsInputs, fs afero.Fs) (present.Delivered, error)
}

// HooksApproach delivers the engine's hook registrations — a first-class
// surface even where the native form is a section of the settings file.
type HooksApproach interface {
	present.Approach
	DeliverHooks(start present.Start, root present.RootKind, in HooksInputs, fs afero.Fs) (present.Delivered, error)
}

// CommandsApproach delivers the engine's slash-command files. files is the
// filesystem it writes through paired with the locks its writers take: the
// managed set in a shared directory is a read-modify-write that locks.
type CommandsApproach interface {
	present.Approach
	DeliverCommands(start present.Start, root present.RootKind, in CommandsInputs, files safefs.Root) (present.Delivered, error)
}

// SkillsApproach delivers the engine's Agent Skills packages, writing and
// locking through files as CommandsApproach does.
type SkillsApproach interface {
	present.Approach
	DeliverSkills(start present.Start, root present.RootKind, in SkillsInputs, files safefs.Root) (present.Delivered, error)
}

// Surfaces is the DERIVED approach table (Base.Surfaces()): per Kind, the
// one approach the engine declared. A Kind absent from the map is one the
// engine has no native form for: the planner REFUSES it (or records an
// accepted loss) — it is never a permitted no-op.
type Surfaces map[present.Kind]present.Approach

// The per-kind inputs, built by delivery from the decoded package.

// ContextInputs is the assembled context text and its content hash, and
// File: the slash path, relative to the root the plan selected for context,
// of the file to write it into ("" = the approach's own file). An approach
// either honours File at that root — the context lands there and Presented
// names it — or refuses with ErrContextFileUnsupported; it never ignores it.
type ContextInputs struct {
	Text []byte
	Hash string
	File string
}

// ErrContextFileUnsupported: a context approach cannot honour
// ContextInputs.File at the root it was handed. It refuses; it never writes
// its own file instead.
var ErrContextFileUnsupported = errors.New("engine: this context approach cannot write a named context file at this root")

// MCPInputs is the MCP server set keyed by the name the engine's file
// registers each under, the session's own endpoint included as a URL entry.
type MCPInputs struct{ Servers map[string]wire.MCPServer }

// SettingsInputs is what the settings surface carries.
type SettingsInputs struct {
	DenyTools    []string
	Statusline   bool
	ShellTimeout ShellTimeout
	Exports      Exports
}

// ShellTimeout is how long the engine's shell tool runs a command in the
// foreground when the model names no timeout (Default), and the longest the
// model may name (Max). Each engine maps it onto its own mechanism; the zero
// value says nothing, and an engine leaves its own defaults alone.
type ShellTimeout struct {
	Default time.Duration
	Max     time.Duration
}

// HooksInputs is the hook set by unified event, the unified→native event
// map, and the hooks declared by NATIVE event per engine name
// (wire.HooksConfig.Ext): an engine delivers its own entry as given.
type HooksInputs struct {
	Hooks     wire.UnifiedHooks
	HookEvent map[string]string
	Ext       map[string]wire.BackendHooks
}

// CommandsInputs is the command export set.
type CommandsInputs struct{ Commands []CommandExport }

// SkillsInputs is the skill export set.
type SkillsInputs struct{ Skills []SkillExport }
