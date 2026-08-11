// Package engine is taskloom's registry of agent MCP registrars, so
// `taskloom manage` can register the `taskloom mcp` server without ctxloom.
// The implementations are the agent modules' own agent.MCPRegistrar types
// (claude/codex/kiro) — engine-specific details (config paths,
// on-disk format) live entirely in each agent's module, never here.
package engine

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/kiro"
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
	return []Engine{claude.MCPRegistrar{}, codex.MCPRegistrar{}, kiro.MCPRegistrar{}}
}

// Get returns the engine for a name: the canonical name or a declared alias,
// case-insensitively. No prefix matching — a typo must error rather than
// silently pick an engine.
func Get(name string) (Engine, error) {
	want := agent.CanonicalEngineName(name)
	for _, e := range All() {
		if e.Name() == want {
			return e, nil
		}
	}
	return nil, fmt.Errorf("unknown engine %q; known engines: %s", name, knownEngineSpellings())
}

// knownEngineSpellings renders the accepted --engine vocabulary for a refusal:
// every registered engine's canonical name, each followed by the alternate
// spellings that also resolve to it. Derived from All() and the shared alias
// table rather than written out as a literal, so an engine added to the
// registry cannot be missing from the message that is supposed to enumerate
// the registry.
func knownEngineSpellings() string {
	names := make([]string, 0, len(All()))
	for _, e := range All() {
		name := e.Name()
		if aliases := agent.EngineNameAliases(name); len(aliases) > 0 {
			name += " (also: " + strings.Join(aliases, ", ") + ")"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
