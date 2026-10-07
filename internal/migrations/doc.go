// Package migrations is where a versioned file kind's schemaver.Steps live,
// one package per instance:
//
//	internal/migrations/<kind>/steps.go              package <kind>mig: Steps(), the kind's chain, oldest first
//	internal/migrations/<kind>/internal/v<N>/step.go package v<N>: the step to generation N
//
// The kind's owner declares schemaver.Define(name, current, <kind>mig.Steps()...).
// Adding a generation is a v<N> package, a line in Steps and the owner's
// current bumped in the same commit as the decode change that is the reason
// for it. Retiring the oldest is deleting its package and its line: Current
// is declared, so nothing else moves, and Define refuses any other gap.
// A kind with no steps declares schemaver.Define(name, current) and imports
// nothing from here.
//
// A step is a leaf: a pure function of one YAML document, reaching only
// schemaver and the yamlx node helpers (archrules row migrations-are-leaves).
// Go's internal rule confines a v<N> package to its own kind's list.
package migrations
