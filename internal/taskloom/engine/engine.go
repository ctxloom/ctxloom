// Package engine is taskloom's registry of agent MCP registrars, so
// `taskloom manage` can register the `taskloom mcp` server without ctxloom.
// The implementations are the agent modules' own agent.MCPRegistrar types
// (claude/antigravity/codex/kiro) — engine-specific details (config paths,
// on-disk format) live entirely in each agent's module, never here.
package engine

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/antigravity"
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

// TaskloomServer is the server `taskloom manage` registers: the command line
// that serves `taskloom mcp`.
func TaskloomServer() wire.MCPServer {
	return wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}}
}

// All returns every known engine, for "register wherever present" flows. A
// fresh slice each call, so a caller mutating its result never corrupts the
// registry.
func All() []Engine {
	return []Engine{claude.MCPRegistrar{}, antigravity.MCPRegistrar{}, codex.MCPRegistrar{}, kiro.MCPRegistrar{}}
}

// engineAliases maps accepted alternate spellings to canonical engine names.
var engineAliases = map[string]string{
	"claudecode": "claude-code",
	"claude":     "claude-code",
	"agy":        "antigravity",
}

// Get returns the engine for a name: the canonical name or a declared alias,
// case-insensitively. No prefix matching — a typo must error rather than
// silently pick an engine.
func Get(name string) (Engine, error) {
	want := strings.ToLower(name)
	if canonical, ok := engineAliases[want]; ok {
		want = canonical
	}
	for _, e := range All() {
		if e.Name() == want {
			return e, nil
		}
	}
	return nil, fmt.Errorf("unknown engine %q", name)
}
