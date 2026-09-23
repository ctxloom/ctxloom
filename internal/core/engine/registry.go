package engine

import (
	"fmt"
	"sort"
)

// Registry is the set of engines a process was composed with: a VALUE built
// at the composition root (engines.Build()), never a package global. It
// refuses a duplicate Name; coherence of each Definition is the engine
// constructor's job (the ONE place), asserted by conformance.
type Registry struct{ m map[Name]Engine }

// NewRegistry composes the engines into a Registry, refusing a duplicate
// name. It does not re-validate: the constructor did.
func NewRegistry(engines ...Engine) (Registry, error) {
	r := Registry{m: map[Name]Engine{}}
	for _, e := range engines {
		d := e.Root()
		if _, dup := r.m[d.Name]; dup {
			return Registry{}, fmt.Errorf("engine %q registered twice", d.Name)
		}
		r.m[d.Name] = e
	}
	return r, nil
}

// Lookup resolves a Name by EXACT match. No alias, case or prefix
// resolution: an engine has one spelling, and any other reaches the caller
// unresolved so it is refused rather than rounded to a real engine.
func (r Registry) Lookup(name Name) (Engine, bool) { e, ok := r.m[name]; return e, ok }

// Names lists, sorted, the registered names keep accepts; a nil keep lists
// every name. Every "the engines that declare X" view is a predicate on the
// Definition, so no caller needs a loop of its own.
func (r Registry) Names(keep func(Definition) bool) []Name {
	var out []Name
	for n, e := range r.m {
		if keep == nil || keep(e.Root().Definition) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// NamesWhere lists, sorted, the registered names whose engine VALUE keep
// accepts — a view over what the values implement, where Names is a view
// over their Definitions.
func (r Registry) NamesWhere(keep func(Name, Engine) bool) []Name {
	var out []Name
	for _, n := range r.Names(nil) {
		if keep(n, r.m[n]) {
			out = append(out, n)
		}
	}
	return out
}

// Default is the engine a process offers when nothing named one: the ONE
// engine shipped with DistributionDefault. Zero or several is a composition
// error, refused by name — a default is never guessed.
func (r Registry) Default() (Engine, error) {
	names := r.Names(func(d Definition) bool { return d.Distribution == DistributionDefault })
	if len(names) != 1 {
		return nil, fmt.Errorf("engine registry: %d engines ship by default (%v); exactly one names the default", len(names), names)
	}
	e, _ := r.Lookup(names[0])
	return e, nil
}
