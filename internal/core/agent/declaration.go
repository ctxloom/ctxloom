package agent

import (
	"maps"
	"slices"
)

// This file is the OPEN SET of delivery approach NAMES: what an engine
// declares a binding may select for each surface. It replaces a closed enum
// whose values were documented by which engine used them — a shared
// vocabulary defined by its consumers is not a vocabulary, it is the first
// engine's shape with every later engine mapped onto it.
//
// The shape follows the open-sets ruling that already governs roots: the SET
// is open and keyed by name; the well-known members stay well-known as NAMED
// CONSTANTS (ApproachFile), never as struct fields or method names; and
// a name the declaration does not carry FAILS LOUD where a binding names it,
// never resolves to a zero value.
//
// It is a static table read by --help, shell completion and binding
// validation, before anything is resolved. Nothing is built from it: the
// engine's typed approaches (engine.Definition) deliver, through the static
// writer (ruled 2026-10-07: retire the construction path to a name table).

// Declaration is an engine's whole static name table: per surface kind, every
// approach name a binding may select and which one it falls back to. It is
// what --help, completion and config validation read.
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
