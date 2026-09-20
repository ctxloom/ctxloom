package backends

import (
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// Configurable is implemented by backends that accept their own typed config.
// The argument is the backend's concrete BackendConfig (decoded by the config
// registry), so no shared code ever type-switches on backend specifics.
type Configurable interface {
	Configure(cfg agent.BackendConfig)
}

// record pairs one engine KIND (its Definition, views, Home, Container and
// Transcripts, built by the engine package's constructor) with the hosting
// record this package still needs to run it. Everything the port carries
// is read off the kind; the hosting record is the remainder.
type record struct {
	kind engine.Engine
	host hosting.Hosting
}

// records is the per-engine table, keyed by the engine's one registered
// name. It holds values authored in each engine's OWN package; nothing in
// this package names an engine.
var records = make(map[string]*record)

// Every backend registered here reaches its model by spawning the VENDOR'S OWN
// agent binary. ctxloom holds no provider SDK and makes no direct call to any
// model API — and must not acquire one on any path that carries subscription
// credentials.
//
// This is a licensing invariant, not a style preference. Anthropic reserves
// subscription OAuth for "ordinary use of Claude Code and other native
// Anthropic applications", bars tools that "misrepresent their identity to
// Anthropic's servers" or "route third-party traffic against subscription
// limits", and directs anyone building on the Agent SDK to API keys instead
// (support article 13189465; code.claude.com legal-and-compliance). Lifting a
// subscription token into our own HTTP client is the prohibited act — it is
// precisely the identity misrepresentation named above. Launching the
// vendor's signed-in binary as a child process is not: the genuine binary
// makes the call, so there is nothing to misrepresent, and Anthropic names
// `claude -p` as drawing on subscription limits, i.e. metered rather than
// banned. Adding anthropic-sdk-go / openai-go / langchaingo "to simplify the
// launcher" would forfeit that standing.
//
// The compliance therefore lives in the SHAPE of every descriptor's
// NewBackend, not in any one engine.
//
// Metered BYO-API-key access through a gateway (OpenRouter, LiteLLM) is fine —
// but a gateway serves Anthropic *models*, never Claude *Code*, and a
// subscription-authenticated CLI cannot be pointed at one.

// Register installs the composed engine kinds with their hosting records:
// every hosting.Hosting must name a kind in reg and every kind must have exactly
// one hosting.Hosting, so an engine can never be declared here and unrunnable, or
// runnable and undeclared. It returns an error rather than panicking:
// registration runs from a composition root that can surface it, not from
// package init.
//
// Two-phase: every record is validated (hosting.Hosting.Validate, plus the
// pairing rules) before any is installed, so a failed batch leaves the
// tables as they were. The kinds were validated by their constructors.
func Register(reg engine.Registry, hostings ...hosting.Hosting) error {
	batch := map[engine.Name]*record{}
	for i := range hostings {
		h := hostings[i]
		if err := h.Validate(); err != nil {
			return err
		}
		kind, ok := reg.Lookup(h.Engine)
		if !ok {
			return fmt.Errorf("hosting %s: no engine kind of that name was composed", h.Engine)
		}
		if _, dup := records[string(h.Engine)]; dup || batch[h.Engine] != nil {
			return fmt.Errorf("hosting %s: already registered", h.Engine)
		}
		batch[h.Engine] = &record{kind: kind, host: h}
	}
	for _, name := range reg.Names(nil) {
		if batch[name] == nil && records[string(name)] == nil {
			return fmt.Errorf("engine %s: composed without a hosting record", name)
		}
	}
	for name, r := range batch {
		records[string(name)] = r
	}
	// The cells adapter reads engine facts through ONE accessor
	// (isolation.Facts) over the engines' own declarations — Engine.Home,
	// Engine.Container — installed here, where the registry is; isolation
	// resolves engines by NAME (CopyAmbient is handed a backend name) and
	// cannot import the engine packages.
	isolation.UseFacts(recordFacts{})
	return nil
}

// recordFacts is the isolation.Facts accessor over the records, read LIVE:
// a record unregistered later is a fact forgotten.
type recordFacts struct{}

func (recordFacts) For(name string) (isolation.EngineFacts, bool) {
	r, ok := records[name]
	if !ok {
		return isolation.EngineFacts{}, false
	}
	return isolation.FactsOf(r.kind), true
}

func (recordFacts) Names() []string {
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	return names
}

// Engines is the engine.Registry of every kind registered here: for a
// reader that wants Definitions (a default, a filtered name list) rather
// than a hosting record. Names are unique by construction, so composing
// them cannot fail.
func Engines() engine.Registry {
	kinds := make([]engine.Engine, 0, len(records))
	for _, r := range records {
		kinds = append(kinds, r.kind)
	}
	reg, err := engine.NewRegistry(kinds...)
	if err != nil {
		panic("backends: " + err.Error())
	}
	return reg
}

// DefaultEngineName is the name of the one engine shipped by default — what
// an untyped llm entry, an init with no choice made, or a scaffold records.
// "" when no engine ships by default, which the composition refuses
// upstream.
func DefaultEngineName() string {
	def, err := Engines().Default()
	if err != nil {
		return ""
	}
	return string(def.Root().Name)
}

// DefaultEngines lists, sorted, the engines shipped by default: the curated
// head of any engine menu.
func DefaultEngines() []string {
	var out []string
	for _, n := range Engines().Names(func(d engine.Definition) bool { return d.Distribution == engine.DistributionDefault }) {
		out = append(out, string(n))
	}
	return out
}

// EngineBinary is the native client binary the named engine's interactive
// grammar launches, "" for an engine with no declared grammar for it (a
// double, or an unregistered name).
func EngineBinary(name string) string {
	d, ok := Definition(name)
	if !ok {
		return ""
	}
	g, ok := engine.CLIFor(d.CLI, engine.Interactive)
	if !ok {
		return ""
	}
	return g.Binary
}

// Definition returns the named engine's root — its Definition and views —
// by EXACT match on the registered name.
func Definition(name string) (engine.Base, bool) {
	r, ok := records[name]
	if !ok {
		return engine.Base{}, false
	}
	return r.kind.Root(), true
}

// lookup resolves name to its hosting record by EXACT match on the
// registered name. No alias, case or prefix resolution: an engine has one
// spelling, and any other reaches the caller unresolved so it is refused
// rather than rounded to a real backend.
func lookup(name string) (*hosting.Hosting, bool) {
	r, ok := records[name]
	if !ok {
		return nil, false
	}
	return &r.host, true
}

// IsTestOnly reports whether name is a registered test/development double
// rather than a shippable engine. Every user-facing enumeration over List()
// filters through this, so registering a new double hides it everywhere at
// once instead of requiring each caller to learn its name.
//
// An unknown name is NOT test-only: callers distinguish "unknown engine" from
// "engine you may not pick" separately, and folding the two here would turn a
// typo into a silent omission.
func IsTestOnly(name string) bool {
	return DistributionFor(name) == engine.DistributionTestOnly
}

// DistributionFor returns the named engine's shipping policy, or Unset for a
// name nobody registered — which no policy check reads as any decision.
func DistributionFor(name string) engine.Distribution {
	d, ok := Definition(name)
	if !ok {
		return engine.DistributionUnset
	}
	return d.Distribution
}

// Get returns a new instance of the named backend, with this package's
// pty-backed launcher injected — the substrate that execs processes lives
// here, not in the engine.
func Get(name string) agent.Backend {
	if d, ok := lookup(name); ok {
		return d.NewBackend(RunLaunchSpec)
	}
	return nil
}

// List returns all registered backend names, sorted — map iteration order is
// randomized per Go's spec, so every caller (shell completion, help output)
// would otherwise have to sort defensively.
func List() []string {
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ListWhere returns the registered backend names keep accepts, in List's
// sorted order. It is the one filter over the registry: every "the backends
// that declare X" view is a predicate on a name, and a loop per view is how
// four copies of this came to exist.
func ListWhere(keep func(name string) bool) []string {
	var names []string
	for _, name := range List() {
		if keep(name) {
			names = append(names, name)
		}
	}
	return names
}

// Exists returns true if a backend with the given name is registered.
func Exists(name string) bool {
	_, ok := lookup(name)
	return ok
}

// PermissionFactsFor reads the named engine's declared permission facts off
// its Definition: the host default posture and whether plan is a genuine
// read-only tier. An unregistered name has the zero facts, which resolve to
// prompt-per-call and collapse plan.
func PermissionFactsFor(name string) engine.PermissionFacts {
	d, ok := Definition(name)
	if !ok {
		return engine.PermissionFacts{}
	}
	return d.Permissions
}

// EnforcesReadOnlyPlan reports whether the named engine declares
// PermissionPlan as a genuinely read-only, non-prompting mode
// (Definition.Permissions.ReadOnlyPlan). An engine that doesn't would run
// plan unrestrained and can't be trusted to be headless-safe for it, so the
// run resolver collapses plan to default for it instead. An unregistered name
// reports false.
func EnforcesReadOnlyPlan(name string) bool { return PermissionFactsFor(name).ReadOnlyPlan }

// BinaryPathProvider is implemented by backends that expose their binary path.
// agent.BaseBackend satisfies it (see agent.BaseBackend.GetBinaryPath), so
// every backend registered in THIS package's descriptor table is a provider.
// The type assertion below might look like it buys nothing on that
// evidence alone, but it is not dead: agent.Backend itself does not require
// GetBinaryPath (internal/lm/grpc/server_test.go's fakeBackend implements
// agent.Backend without it), so widening the interface directly would break
// that non-BaseBackend-embedding implementor. The optional-capability
// assertion here is the correct shape for a genuinely optional capability,
// not dead defensiveness.
type BinaryPathProvider interface {
	GetBinaryPath() string
}

// GetDefaultBinary returns the default binary name for a backend by instantiating it.
func GetDefaultBinary(name string) string {
	backend := Get(name)
	if backend == nil {
		return ""
	}
	if provider, ok := backend.(BinaryPathProvider); ok {
		return provider.GetBinaryPath()
	}
	return ""
}

// AvailabilityOf resolves the named backend's default binary and reports
// where it was found on PATH (or the login-shell PATH fallback), or the
// reason it could not be — IsAvailable's plain bool used to collapse
// "unregistered backend", "backend has no default binary", and "binary not
// resolvable on PATH" into the same false, leaving a caller like
// `ctxloom init` (which uses IsAvailable to decide which engines to offer)
// with no way to explain why one is missing.
func AvailabilityOf(name string) (string, error) {
	binary := GetDefaultBinary(name)
	if binary == "" {
		return "", fmt.Errorf("backend %q has no default binary to resolve", name)
	}
	return shellenv.Resolve(binary)
}

// IsAvailable returns true if the backend's default binary is installed and
// resolvable — via the process's own inherited PATH, or (shellenv.Resolve's
// fallback) the user's login-shell PATH, so a GUI-launched ctxloom (minimal
// inherited PATH) reports the same availability a terminal-launched one
// would. A thin boolean convenience over AvailabilityOf; use that directly
// when the reason for unavailability matters.
func IsAvailable(name string) bool {
	_, err := AvailabilityOf(name)
	return err == nil
}
