// Package engine is taskloom's registry of agent MCP registrars, so
// `taskloom manage` can register the `taskloom mcp` server without ctxloom.
// The implementations are the agent modules' own agent.MCPRegistrar types
// — engine-specific details (config paths,
// on-disk format) live entirely in each agent's module, never here.
package engine

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// Engine is the per-agent MCP registration contract, defined in shared/agent
// and implemented by each agent module.
type Engine = agent.MCPRegistrar

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
