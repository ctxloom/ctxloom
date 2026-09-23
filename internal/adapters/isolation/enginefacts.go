package isolation

import (
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// EngineFacts are the facts about one engine the cells adapter reads to
// prepare a cell: how its home relocates and how a run there authenticates
// (Engine.Home), how a container of it is built and authenticated
// (Engine.Container), and whether it ships by default. They are the ENGINE'S
// OWN declarations, read off the engine value through ONE port.
type EngineFacts struct {
	Home engine.HomeSpec
	// Container is the engine's container story, or its refusal: an engine
	// with no image refuses Container() with ErrUnsupported, and that
	// refusal is the absent reason a container binding is refused with.
	Container    engine.Declared[engine.ContainerSpec]
	Distribution engine.Distribution
}

// FactsOf projects one engine's declarations into the facts the cells
// adapter reads.
func FactsOf(eng engine.Engine) EngineFacts {
	f := EngineFacts{Home: eng.Home(), Distribution: eng.Root().Distribution}
	if spec, err := eng.Container(); err != nil {
		f.Container = engine.Absent[engine.ContainerSpec](err.Error())
	} else {
		f.Container = engine.Provide(spec)
	}
	return f
}

// Facts is the one accessor the cells adapter reads engine facts through:
// the facts for a named engine, and the names it can answer for. Isolation
// resolves engines by NAME (PrepareInstanceHome is handed an engine name) and never
// imports an engine package; the composition root installs the accessor
// once (UseFacts).
type Facts interface {
	For(name string) (EngineFacts, bool)
	Names() []string
}

var (
	factsMu sync.RWMutex
	facts   Facts
)

// UseFacts installs the accessor. Called once by the composition root that
// built the registry; a test installs an overlay and restores what it
// replaced.
func UseFacts(f Facts) (restore func()) {
	factsMu.Lock()
	defer factsMu.Unlock()
	prev := facts
	facts = f
	return func() {
		factsMu.Lock()
		defer factsMu.Unlock()
		facts = prev
	}
}

// factsFor reads one engine's facts; ok is false for an engine the
// accessor does not know, or when none is installed.
func factsFor(name string) (EngineFacts, bool) {
	factsMu.RLock()
	f := facts
	factsMu.RUnlock()
	if f == nil {
		return EngineFacts{}, false
	}
	return f.For(name)
}

// factNames lists, sorted, every engine the accessor answers for.
func factNames() []string {
	factsMu.RLock()
	f := facts
	factsMu.RUnlock()
	if f == nil {
		return nil
	}
	names := f.Names()
	sort.Strings(names)
	return names
}

// engineContainerRegistration is one engine's container story with the
// shipping policy that decides whether it composes into the default image.
type engineContainerRegistration struct {
	container    engine.Declared[engine.ContainerSpec]
	distribution engine.Distribution
}

func engineContainerDeclared(name string) (engineContainerRegistration, bool) {
	f, ok := factsFor(name)
	if !ok {
		return engineContainerRegistration{}, false
	}
	return engineContainerRegistration{container: f.Container, distribution: f.Distribution}, true
}

// registeredEngineContainers is every engine with a decided container story.
func registeredEngineContainers() map[string]engineContainerRegistration {
	out := map[string]engineContainerRegistration{}
	for _, name := range factNames() {
		if r, ok := engineContainerDeclared(name); ok {
			out[name] = r
		}
	}
	return out
}

// RegistryFacts is the Facts accessor over a composed engine.Registry: the
// composition root installs `UseFacts(RegistryFacts{reg})` beside the
// registry, so the cells adapter reads the same kinds every other adapter
// resolves by name.
type RegistryFacts struct{ Registry engine.Registry }

func (r RegistryFacts) For(name string) (EngineFacts, bool) {
	e, ok := r.Registry.Lookup(engine.Name(name))
	if !ok {
		return EngineFacts{}, false
	}
	return FactsOf(e), true
}

func (r RegistryFacts) Names() []string {
	var names []string
	for _, n := range r.Registry.Names(nil) {
		names = append(names, string(n))
	}
	return names
}
