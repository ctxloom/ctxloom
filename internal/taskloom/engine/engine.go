// Package engine is taskloom's registry of agent MCP registrars, so
// `taskloom manage` can register the `taskloom mcp` server without ctxloom.
// The implementations are the agent modules' own registrar types —
// engine-specific details (config paths, on-disk format, the container the
// servers live in) live entirely in each agent's module, never here.
package engine

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// Engine is the MCP-registration facet of an agent: where its MCP config lives
// per scope and how one named server is written into / taken out of that file.
// It is deliberately separate from agent.SettingsWriter — the writer reconciles
// the ctxloom-managed server set against whole files, while a registrar gives
// an external tool (taskloom manage) single-server registration.
//
// It lives here rather than in shared/agent because a registrar writes through
// confpatch, and confpatch depends on shared/agent.
type Engine interface {
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
	// Register writes the named server into the config at path through store,
	// patching only that member and recording what it wrote so the next
	// Register can take it back out; a nil server is the uninstall. Foreign
	// keys and servers survive byte for byte. Idempotent: a Register that
	// changes nothing writes nothing and leaves no record.
	Register(fs afero.Fs, store *confpatch.Store, path, name string, server *wire.MCPServer, opts ...confpatch.ApplyOption) (confpatch.Result, error)
	// Installed reports whether the named server is present in the config.
	Installed(config []byte, name string) (bool, error)
}

// TaskloomName is the key the registration installs under.
const TaskloomName = "taskloom"

// TaskloomCommand is the command a registered entry names. It is a BARE name,
// resolved against whatever PATH the agent process holds when it later starts
// the server — not an absolute path captured at registration time, so a config
// written once still resolves after the binary moves (an upgrade in place, a
// different home on a bind-mounted config, a container image whose taskloom
// lives elsewhere). The price of that choice is that registering proves
// nothing about whether the server will ever start, which is what
// VerifyCommandResolvable exists to say out loud.
const TaskloomCommand = "taskloom"

// TaskloomServer is the server `taskloom manage` registers: the command line
// that serves `taskloom mcp`.
func TaskloomServer() wire.MCPServer {
	return wire.MCPServer{Command: TaskloomCommand, Args: []string{"mcp"}}
}

// VerifyCommandResolvable reports whether TaskloomCommand resolves on the
// CURRENT process's PATH, so a caller that has just written a registration can
// say whether the entry it wrote names anything reachable. A failure here is
// not proof the server will fail — the agent may run with a richer PATH than
// this process, which is precisely why registration cannot simply refuse — but
// a success here is the strongest evidence available at registration time, and
// without it the only signal a user ever gets is a server that silently never
// starts, hours later and nowhere near the command that promised it.
func VerifyCommandResolvable() error {
	if _, err := exec.LookPath(TaskloomCommand); err != nil {
		return fmt.Errorf("%q does not resolve on this process's PATH: %w", TaskloomCommand, err)
	}
	return nil
}

// All returns every known engine, for "register wherever present" flows. A
// fresh slice each call, so a caller mutating its result never corrupts the
// registry.
func All() []Engine {
	return []Engine{claude.MCPRegistrar{}}
}

// Get returns the engine registered under exactly name. There is no alias,
// case or prefix matching — a typo must error rather than silently pick an
// engine.
func Get(name string) (Engine, error) {
	for _, e := range All() {
		if e.Name() == name {
			return e, nil
		}
	}
	return nil, fmt.Errorf("unknown engine %q; known engines: %s", name, strings.Join(Names(), ", "))
}

// Names lists every registered engine's canonical name: the vocabulary
// --engine accepts, for help text that advertises it. Derived from All()
// rather than written out as a literal, so an engine added to the registry
// cannot be missing from the text that is supposed to enumerate the registry.
func Names() []string {
	names := make([]string, 0, len(All()))
	for _, e := range All() {
		names = append(names, e.Name())
	}
	return names
}
