package agent

import (
	"maps"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is the OPEN SET of delivery approaches: what an engine hands over
// to say "I can deliver this surface this way". It replaces a closed enum
// whose values were documented by which engine used them — a shared
// vocabulary defined by its consumers is not a vocabulary, it is the first
// engine's shape with every later engine mapped onto it, and adding one that
// did not fit meant editing the enum, its ordering list and its parser.
//
// The shape follows the open-sets ruling that already governs roots: the SET
// is open and keyed by name; the well-known members stay well-known as NAMED
// CONSTANTS (ApproachUnsafeFile, ApproachHook), never as struct fields or
// method names; and a name the declaration cannot construct FAILS LOUD, never
// resolves to a zero value that delivers nothing while reporting success.
//
// Three phases, kept apart on purpose:
//
//	REGISTRATION  package init. A name maps straight to a Construct — a
//	              constructor, so "supported" and "constructible" are one fact.
//	              Carries no roots, no filesystem state, nothing run-specific.
//	CONSTRUCTION  per run. The Construct receives THAT RUN's content; roots
//	              reach the built Approach later, at Present/Deliver time,
//	              through the same advised present.Start the writer already
//	              takes. A worktree run, a host run and a container run build
//	              from ONE registration and land in DIFFERENT places.
//	ENUMERATION   Names and Default read registration ONLY. --help and shell
//	              completion call them before anything is resolved, so nothing
//	              they read may require a built Approach or a root.

// Approach is ONE way one surface's bytes reach an engine. An engine package
// supplies one per delivery mechanism it actually supports and says nothing
// about the ones it cannot; shared code never enumerates them and never names
// an engine-specific one.
type Approach interface {
	// Present composes where this approach's bytes land beneath the ADVISED
	// roots and how the engine is told about them (argv / env). It is pure
	// with respect to the filesystem. Because the Approach was CONSTRUCTED
	// from the run's content, Present can name a content-derived leaf
	// truthfully — which a static, content-free presenter provably could not.
	Present(start present.Start) present.Presentation
}

// Construct builds one Approach for ONE RUN from that run's content, writing
// through files (its filesystem paired with the locks its writers take). It
// carries no present roots: the built Approach receives them at
// Present/Deliver time.
type Construct func(in SurfaceInputs, files safefs.Root) Approach

// Declaration is an engine's whole static declaration — registration, phase
// one. Per surface kind, every approach the engine can construct for it and
// which one it falls back to. It is what --help, completion and config
// validation read, and none of them build anything from it.
//
// A kind absent from the map is absent or folded for that engine (an engine
// whose MCP rides its settings file declares no SurfaceMCP; every shipped
// engine folds SurfaceHooks into its settings file and declares none):
// selecting it is a permitted no-op, never an error.
type Declaration map[SurfaceKind]Presentations

// Names lists the approach names declared for kind, sorted; nil when the kind
// is absent. Pure.
func (d Declaration) Names(kind SurfaceKind) []string {
	p, ok := d[kind]
	if !ok {
		return nil
	}
	return p.Names()
}

// Default reports the default approach name for kind, or false when the kind
// is absent. Pure.
func (d Declaration) Default(kind SurfaceKind) (string, bool) {
	p, ok := d[kind]
	if !ok {
		return "", false
	}
	return p.Default(), true
}

// AllNames is the union of every approach name across every kind, sorted:
// what a CLI can offer as "names that exist at all" before an engine is
// chosen. Pure.
func (d Declaration) AllNames() []string { return ApproachNames(d) }

// ApproachNames is the union of every approach name across every kind of
// every declaration handed to it, sorted — the one union, whether over one
// engine's declaration or every registered engine's.
func ApproachNames(decls ...Declaration) []string {
	seen := map[string]struct{}{}
	for _, d := range decls {
		for _, p := range d {
			for _, n := range p.Names() {
				seen[n] = struct{}{}
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// Forms is implemented by a typed engine approach (a field of
// engine.Definition) that still delivers through this seam's named
// Presentations: the runtime forms the launch path constructs by name. It is
// how DeclarationOf derives the Declaration from the Definition, so an
// engine keeps ONE table. It leaves with this seam.
type Forms interface{ Forms() Presentations }

// DeclarationOf derives the Declaration this seam reads from an engine's
// derived surface table: every typed approach that carries Forms contributes
// its named Presentations under its kind. An approach without Forms has no
// runtime form here and is simply absent from the Declaration.
func DeclarationOf(s engine.Surfaces) Declaration {
	d := Declaration{}
	for kind, a := range s {
		if f, ok := a.(Forms); ok {
			d[kind] = f.Forms()
		}
	}
	return d
}
