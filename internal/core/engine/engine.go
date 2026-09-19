// Package engine is the engine port's home: the vocabulary an engine is
// named, driven and postured in. It must never know launch, delivery,
// composite, config, isolation or the wire — nothing above it names a type
// here that an engine implements against. Today it holds the vocabulary
// only; the engine base under core/agent stands in for the rest of the port
// until its contract half moves here.
package engine

import "strconv"

// Name is the registry key and the ONLY spelling of an engine.
type Name string

// String renders the key.
func (n Name) String() string { return string(n) }

// Mode is how a run is driven: Interactive = a pty; Structured = the engine's
// native structured protocol.
type Mode int

const (
	Interactive Mode = iota + 1
	Structured
)

// String renders the mode; an unknown value is visibly bad, never silently
// one of the two.
func (m Mode) String() string {
	switch m {
	case Interactive:
		return "interactive"
	case Structured:
		return "structured"
	default:
		return "mode(" + strconv.Itoa(int(m)) + ")"
	}
}
