package agent

import (
	"sort"

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

// OutOfCwd is implemented by an Approach that ALSO has a race-safe form: the
// same surface written OUT of the shared working directory (beneath the
// advised Scratch root, announced to the engine by a launch flag) instead of
// at its well-known path. A shared-cwd delivery runs this form in place of
// Deliver, without the race warning; a shared launch with no stated
// preference prefers an approach that has one over the at-rest default.
//
// It is RESIDUE, and settings is the last approach that has one. Being a second
// FORM rather than a separate approach is what made the conversion invisible:
// a caller that named the well-known file got the private one instead, on a
// shared launch, and was told it succeeded — while the SAME selection on an
// isolated cell got the well-known file. One name, two behaviours, neither of
// them the caller's choice. Context and MCP were split into separate approaches
// for that reason; settings cannot be split while an isolated cell's scratch IS
// its checkout, because a private --settings file would land at the well-known
// path AND be announced on the flag, registering claude's hooks twice.
//
// Omitting it is the safe direction: an approach without it is warned and not
// preferred, never silently treated as race-free. Prefer declaring one
// single-form approach per behaviour over adding a second form here.
type OutOfCwd interface {
	DeliverIsolated(start present.Start) (Delivered, error)
}

// Existing is implemented by an Approach whose out-of-cwd form can be NAMED
// without being written, because where it lands is a function of the run's
// content alone (a content-addressed leaf, or a well-known name beneath the
// advised Scratch root).
//
// It is the seam LaunchFormPresent runs. A member sharing the project cwd must
// not rewrite the session's one surface set, but it still has to tell the
// engine where those surfaces are — and telling requires naming them. Present
// is already documented as pure with respect to the filesystem and as composing
// "how the engine is told about them (argv / env)", so naming is its job;
// PresentExisting is the half that needs the run's own roots to resolve a path
// and the run's own filesystem to insist the path is real.
//
// Omitting it is the safe direction: an approach without it contributes no argv
// under LaunchFormPresent, exactly as an approach that delivered nothing
// contributes none under LaunchFormDeliver. Silence about a surface is never
// the same as a flag naming a file that is not there.
type Existing interface {
	// PresentExisting records and returns the path this approach's out-of-cwd
	// form occupies — the same path DeliverIsolated would have written — having
	// written nothing itself.
	//
	// "" with a nil error means this approach has no bytes this run (empty
	// context, an empty MCP set) and so nothing to present; the caller emits no
	// flag for it. A path that does NOT exist is an error wrapping
	// ErrAbsentSharedSurface — never a fallback to writing it.
	PresentExisting(start present.Start) (string, error)
}

// MinimalLaunch is implemented by an engine that declares a MINIMAL launch
// posture: the argv that strips it back to a bare model call — no hooks, no
// slash commands, no project memory, no session persistence — for a headless
// run that delivers no managed surfaces at all (LaunchFormMinimal).
//
// It is a DECLARATION resolved by Setup into the run's launch argv, not a
// branch Execute takes on a request flag. The engine says what its minimal
// posture IS, once; every argv site then emits what Setup resolved. An engine
// that has no such posture does not implement it and a minimal run launches it
// bare.
type MinimalLaunch interface {
	MinimalArgs(model string) []string
}

// MinimalArgsFunc adapts a plain function to MinimalLaunch, so an engine can
// register its posture without declaring a type whose only purpose is to hold
// one method.
type MinimalArgsFunc func(model string) []string

// MinimalArgs implements MinimalLaunch.
func (f MinimalArgsFunc) MinimalArgs(model string) []string { return f(model) }

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
func (d Declaration) AllNames() []string {
	seen := map[string]bool{}
	for _, p := range d {
		for _, n := range p.Names() {
			seen[n] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
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
