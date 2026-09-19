package agent

import (
	"sort"

	"github.com/spf13/afero"
)

// This file is the CONFIG-AUTHORED construction path for engine surface
// presentation — the counterpart to the program-authored typestate chain in
// the present subpackage.
//
// The two paths exist because authorship differs, not because one is a weaker
// copy of the other. A composition written in Go is seen by the compiler, so
// present makes an illegal one UNWRITABLE and its Build TOTAL. A composition
// authored in USER CONFIG is seen by no compiler: an agent-binding file on
// disk names a delivery as a bare string. Completeness there cannot be proven
// at build time, so it is established at RESOLVE time instead: the name is
// looked up in the engine's Declaration, and a name it cannot construct is
// refused where it entered — a ClassConfig finding at config load, a loud
// Build error at delivery. Neither path falls back to the default behind the
// caller's back.

// Presentations is one engine's declared approaches for ONE surface: every
// delivery that engine can construct for that surface, and which of them it
// falls back to.
//
// It replaces a capability LIST plus a separate construction map. That split
// is what made the enum expensive: a name could be listed as supported while
// no surface existed to build it, so "supported" and "constructible" were two
// facts that could disagree, and a user-facing failure could come from either.
// Here a name is known IF AND ONLY IF a Construct is registered under it —
// support is not recorded anywhere, it is the presence of the thing that does
// the work. The disagreement is therefore not merely caught; it is unwritable.
//
// The vocabulary is DERIVED from the registered constructors (see Names),
// never declared beside them, so it cannot drift from what the engine can
// actually build.
type Presentations struct {
	engine string
	kind   SurfaceKind
	// def is the name resolution falls back to. It is a NAMED key into byName,
	// not a separate constructor and not a positional convention: a default
	// that were its own constructor could build something no user is able to
	// ask for, and a default that were "the first entry declared" would make
	// the order of a literal load-bearing. Presents takes it as a required
	// argument, so a Presentations without a default does not exist.
	def    string
	byName map[string]Construct
}

// Presents begins an engine's declaration for one surface with its DEFAULT.
// The default is the required first delivery rather than a field set later
// because both of its invariants then hold by construction: every
// Presentations has one, and it names something the engine can actually build.
func Presents(engine string, kind SurfaceKind, defaultName string, c Construct) Presentations {
	return Presentations{
		engine: engine,
		kind:   kind,
		def:    defaultName,
		byName: map[string]Construct{defaultName: c},
	}
}

// Or declares one more delivery the engine can construct for this surface.
//
// It copies rather than mutating in place so that a Presentations shared as a
// value cannot have deliveries added to it through an alias — declarations are
// built once and then read concurrently by whatever launches.
func (d Presentations) Or(name string, c Construct) Presentations {
	next := make(map[string]Construct, len(d.byName)+1)
	for k, v := range d.byName {
		next[k] = v
	}
	next[name] = c
	d.byName = next
	return d
}

// Names lists every delivery this engine can construct for this surface,
// sorted. It is derived from the registered constructors, so it cannot claim
// support the engine does not have.
//
// It is a PURE function of the declaration: no environment, no roots, nothing
// constructed. Help text and completion call it before anything is resolved.
//
// Sorted, and not in declaration order, on purpose: order carries NO meaning
// here. The default is a named field (Default), so leaving declaration order
// visible would invite a reader to infer a ranking from it that nothing
// honours. Every reader that needs the default asks Default and compares by
// identity; none reads a position.
func (d Presentations) Names() []string {
	names := make([]string, 0, len(d.byName))
	for name := range d.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Default reports the delivery name resolution falls back to. Pure, for the
// same reason Names is.
func (d Presentations) Default() string { return d.def }

// Engine reports which engine declared these presentations, for error text.
func (d Presentations) Engine() string { return d.engine }

// Construct builds the named approach from a run's content. false means the
// name is not declared; the caller names the failure (see the file doc).
func (d Presentations) Construct(name string, in SurfaceInputs, fs afero.Fs) (Approach, bool) {
	c, ok := d.byName[name]
	if !ok {
		return nil, false
	}
	return c(in, fs), true
}
