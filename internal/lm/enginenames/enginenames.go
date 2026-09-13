// Package enginenames is the LEAN composition root of engine NAMES: the one
// production list, for a binary that must resolve an engine name but must
// not link the engine descriptors (ltk and taskloom), of each engine
// package's own name and alternate spellings. ctxloom composes the same
// declarations through the full root (internal/lm/engines, whose registry
// populates the alias table from each descriptor); this root exists so the
// lean binaries compose them too, and agent.CanonicalEngineName cannot
// diverge per binary.
//
// Adding an engine is adding its lean name fact here AND its descriptor to
// internal/lm/engines; tests/arch holds the two roots equal.
package enginenames

import (
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Engine is one engine's name declaration, as its own package states it.
type Engine struct {
	Name    string
	Aliases []string
}

// Declared returns every shipped engine's name declaration — the list this
// root composes, exposed read-only so the gate that holds it equal to the
// descriptor root can read it.
func Declared() []Engine {
	return []Engine{
		{Name: claude.EngineName, Aliases: claude.EngineAliases()},
	}
}

// Register composes every shipped engine's spellings into the process-wide
// alias table. Idempotent per process: an identical mapping registered
// twice is a no-op, so a binary that also composes the full root sees one
// registration.
func Register() error {
	for _, e := range Declared() {
		if err := agent.RegisterEngineAliases(e.Name, e.Aliases); err != nil {
			return err
		}
	}
	return nil
}

// MustRegister is Register for a main or TestMain, where a composition
// failure has no caller to return to and a panic is the correct loud failure.
func MustRegister() {
	if err := Register(); err != nil {
		panic("enginenames: " + err.Error())
	}
}
