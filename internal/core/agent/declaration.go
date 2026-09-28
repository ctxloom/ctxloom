package agent

import (
	"maps"
	"slices"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
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
	// Deliver is the writer seam, unchanged: write beneath the advised roots
	// and return the handle that reverses it. A nil handle means nothing was
	// written (the seam's existing convention).
	Deliver(start present.Start) (Delivered, error)
}

// Construct builds one Approach for ONE RUN from that run's content. It
// carries no roots: the built Approach receives them at Present/Deliver time.
type Construct func(in SurfaceInputs, fs afero.Fs) Approach

// LaunchOnly is implemented by an Approach whose bytes reach the engine only
// through a launch — its out-of-cwd form is announced on argv, and an at-rest
// delivery (materialize, apply, remove) has no argv sink to hand that flag to.
// DeliverUnder refuses it, naming the surface; selecting it there is a caller
// error, not a launch.
type LaunchOnly interface{ LaunchOnly() }

// Rider is implemented by an Approach that writes no bytes of its own and
// RIDES another surface's write — hook-carried context rides the surface
// whose writer emits the hook registrations. Build refuses a selection naming
// a Rider without its ridden kind: a rider delivered alone would report
// success having carried nothing.
type Rider interface{ Rides() SurfaceKind }

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

// Construct builds the named approach for kind from a run's content. false
// means the engine declares no such (kind, name): the caller decides the
// failure — Build errors, a config loader raises its finding. There is
// deliberately no fallback here: a name that resolved to the default behind
// the caller's back would deliver a different presentation than the one
// asked for, and that is the silent substitution this file exists to refuse.
func (d Declaration) Construct(kind SurfaceKind, name string, in SurfaceInputs, fs afero.Fs) (Approach, bool) {
	p, ok := d[kind]
	if !ok {
		return nil, false
	}
	return p.Construct(name, in, fs)
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
