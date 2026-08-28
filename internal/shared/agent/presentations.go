package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
// at build time, so it is established at RESOLVE time instead, and the failure
// is a strictness finding rather than a type error.
//
// What an unknown name does, and why it is not simply an error: it is a
// ClassConfig finding — fatal by default, degradable under --degraded, where
// it falls back to the engine's declared default. Launching with a different
// presentation of the same bytes is not itself the harm, so the launch is
// allowed to proceed once the user has said they accept less. A boundary
// breach would use FailAlways; this is deliberately not one.

// PresentInputs is the resolved environment a Presenter composes against —
// every root an engine might build from, all settled before any Presenter
// runs.
//
// It is a struct rather than a parameter list so that a further root can be
// added later without changing the Presenter signature, and with it every
// engine's declaration. Only roots something actually builds from belong here.
//
// HOST versus ENGINE NAMESPACE is the distinction to keep straight, and the
// field names carry it: a Host value is a path on the machine ctxloom runs on,
// a Target value is that same location as the ENGINE sees it. The present
// package draws the same line twice — Presentation as HostPath vs EnginePath,
// Mount as HostDir vs TargetDir — and these names are meant to be read
// alongside those.
type PresentInputs struct {
	// ProjectRoot is the resolved project root, on the host.
	ProjectRoot string

	// EngineHomeHost is where the engine's home is MATERIALIZED on the host:
	// the directory the engine's own home variable is pointed at.
	EngineHomeHost string

	// EngineHomeTarget is where that same home is MOUNTED in the engine's
	// namespace.
	//
	// When nothing remaps it — a host run, no container — it EQUALS
	// EngineHomeHost rather than being empty, so a Presenter may always compose
	// from it without first asking whether a mapping happened. Resolve applies
	// that rule, so a caller that remaps nothing may leave this unset; a caller
	// that DOES containerize must set it, because nothing downstream can know
	// the mount point it chose.
	//
	// The rule is enforced rather than merely documented because the failure it
	// prevents is silent. An empty root does not produce an obviously broken
	// path — it leaves the result RELATIVE, since filepath.Join("", "context.md")
	// is "context.md". That reaches argv as a well-formed string, which the
	// engine then resolves against whatever working directory it happens to
	// have, and so names the wrong file rather than failing.
	EngineHomeTarget string
}

// mapped applies the unmapped-home rule described on EngineHomeTarget.
//
// It runs once, in Resolve, so that every Presenter may treat EngineHomeTarget
// as populated. Left to each engine to remember, the omission would not fail
// here — it would surface as a wrong path on argv, far from its cause.
func (in PresentInputs) mapped() PresentInputs {
	if in.EngineHomeTarget == "" {
		in.EngineHomeTarget = in.EngineHomeHost
	}
	return in
}

// Presenter builds ONE presentation of one surface.
//
// It receives the resolved ENVIRONMENT and begins the composition itself,
// rather than being handed a start already rooted somewhere. That inversion is
// the point of the type: engines do not agree on what they root on. One
// materializes under the project root; another under a relocated home it names
// with its own environment variable. A pre-rooted start can express only the
// first, so the second would have to close over a runtime value where it is
// DECLARED — which would stop an engine's supported deliveries from being a
// static package-level literal, and stop Names and Default from being
// answerable without constructing something out of placeholder values. Help
// text and shell completion need exactly that answer, and need it without an
// environment.
//
// It returns a Presentation and no error. That is inherited, not overlooked —
// present.Build is total, so an engine that reached a buildable state has
// nothing left to fail at. Resolving the environment is the caller's business
// and has already succeeded by the time a Presenter runs.
type Presenter func(PresentInputs) present.Presentation

// Presentations is one engine's declared presentations of ONE surface: every
// delivery that engine can construct for that surface, and which of them it
// falls back to.
//
// It replaces a capability LIST plus a separate construction map. That split
// is what made the enum expensive: a name could be listed as supported while
// no surface existed to build it, so "supported" and "constructible" were two
// facts that could disagree, and a user-facing failure could come from either.
// Here a name is known IF AND ONLY IF a Presenter is registered under it —
// support is not recorded anywhere, it is the presence of the thing that does
// the work. The disagreement is therefore not merely caught; it is unwritable.
//
// The vocabulary is DERIVED from the registered Presenters (see Names), never
// declared beside them, so it cannot drift from what the engine can actually
// build.
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
	byName map[string]Presenter
}

// Presents begins an engine's declaration for one surface with its DEFAULT.
// The default is the required first delivery rather than a field set later
// because both of its invariants then hold by construction: every
// Presentations has one, and it names something the engine can actually build.
func Presents(engine string, kind SurfaceKind, defaultName string, p Presenter) Presentations {
	return Presentations{
		engine: engine,
		kind:   kind,
		def:    defaultName,
		byName: map[string]Presenter{defaultName: p},
	}
}

// Or declares one more delivery the engine can construct for this surface.
//
// It copies rather than mutating in place so that a Presentations shared as a
// value cannot have deliveries added to it through an alias — declarations are
// built once and then read concurrently by whatever launches.
func (d Presentations) Or(name string, p Presenter) Presentations {
	next := make(map[string]Presenter, len(d.byName)+1)
	for k, v := range d.byName {
		next[k] = v
	}
	next[name] = p
	d.byName = next
	return d
}

// Names lists every delivery this engine can construct for this surface,
// sorted. It is derived from the registered Presenters, so it cannot claim
// support the engine does not have.
//
// It is a PURE function of the declaration: no environment, no roots, nothing
// constructed. Help text and completion call it before anything is resolved.
//
// Sorted, and not in declaration order, on purpose: order carries NO meaning
// here. The default is a named field, so leaving declaration order visible
// would invite a reader to infer a ranking from it that nothing honours.
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

// Resolve constructs the presentation config asked for by name, against the
// resolved environment.
//
// An unrecognised name raises a ClassConfig finding and falls back to the
// engine's default. There is deliberately NO branch on degraded state here and
// no strict parameter: strictness owns fatal-vs-warn centrally, so this
// returns the same presentation either way and the startup gate decides
// whether the launch proceeds. A check that decided its own fatality would be
// a second policy, free to disagree with the first.
func (d Presentations) Resolve(requested string, in PresentInputs) present.Presentation {
	in = in.mapped()
	if p, ok := d.byName[requested]; ok {
		return p(in)
	}
	strictness.FailOnce(strictness.ClassConfig,
		fmt.Sprintf("set the %s surface to one of %s, or remove it to take %s's default (%s)",
			d.kind, strings.Join(d.Names(), ", "), d.engine, d.def),
		"%s: unknown %s delivery %q", d.engine, d.kind, requested)
	return d.byName[d.def](in)
}
