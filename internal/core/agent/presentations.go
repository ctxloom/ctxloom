package agent

import (
	"slices"
)

// This file is the CONFIG-AUTHORED half of engine surface delivery: the
// approach NAMES an agent binding may select. A binding on disk names a
// delivery as a bare string, which no compiler sees, so the name is checked
// at RESOLVE time against the engine's Declaration — a name the engine does
// not declare is refused where it entered (operations.ResolveAgentSurfaces),
// never rounded to the default behind the caller's back.
//
// It is a static table of names and nothing more. Delivery itself is the
// engine's typed approaches (engine.Definition) driven by the one static
// writer (fsstatic); nothing is constructed from a name here.

// Presentations is one engine's declared approach names for ONE surface, and
// which of them it falls back to.
type Presentations struct {
	// def is the name resolution falls back to. It is a NAMED member of names,
	// not a position: Presents takes it as a required argument, so a
	// Presentations without a default does not exist.
	def   string
	names []string // sorted, unique, contains def
}

// Presents declares an engine's approach names for one surface: the DEFAULT
// first, then any others. The default is required, so every Presentations has
// one and it is always among the declared names.
func Presents(defaultName string, others ...string) Presentations {
	names := append([]string{defaultName}, others...)
	slices.Sort(names)
	return Presentations{def: defaultName, names: slices.Compact(names)}
}

// Names lists every approach name declared for this surface, sorted. It is a
// pure read of the table: help text and completion call it before anything is
// resolved. The slice is the caller's own.
//
// Sorted, and not in declaration order, on purpose: order carries NO meaning
// here. The default is a named field (Default), so leaving declaration order
// visible would invite a reader to infer a ranking from it that nothing
// honours.
func (d Presentations) Names() []string { return slices.Clone(d.names) }

// Default reports the name resolution falls back to. Pure, for the same
// reason Names is.
func (d Presentations) Default() string { return d.def }
