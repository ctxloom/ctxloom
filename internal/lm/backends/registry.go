package backends

import (
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	coreengine "github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/lm/engine"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// Configurable is implemented by backends that accept their own typed config.
// The argument is the backend's concrete BackendConfig (decoded by the config
// registry), so no shared code ever type-switches on backend specifics.
type Configurable interface {
	Configure(cfg agent.BackendConfig)
}

// descriptors is the per-engine descriptor table, keyed by the engine's one
// registered name. It holds engine.Descriptor values authored in each
// engine's OWN package; nothing in this package names an engine.
var descriptors = make(map[string]*engine.Descriptor)

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

// Register installs complete engine descriptors. It returns an error rather
// than panicking: registration runs from a composition root that can surface
// it, not from package init, so a bad declaration is found at composition
// time and named — never at package-load time in whichever engine package
// happened to init first.
//
// Two-phase: every descriptor is validated (engine.Descriptor.Validate, plus
// the duplicate-name rule, which needs the table) before any is installed,
// so a failed batch leaves the tables as they were.
func Register(descs ...engine.Descriptor) error {
	batch := map[string]bool{}
	for i := range descs {
		d := descs[i]
		if err := d.Validate(); err != nil {
			return err
		}
		if _, dup := descriptors[d.Name]; dup || batch[d.Name] {
			return fmt.Errorf("descriptor %s: already registered", d.Name)
		}
		batch[d.Name] = true
	}
	for i := range descs {
		d := descs[i]
		descriptors[d.Name] = &d
		// Push the engine-owned isolation facts down to internal/adapters/isolation
		// at the same moment, so a backend can never be launchable here while
		// invisible there. isolation resolves engines by NAME (CopyAmbient is
		// handed a backend name, never an engine value) and cannot import the
		// engine packages, so this is the only direction the wiring can run.
		//
		// The credential seed and the container story are pushed for EVERY
		// descriptor, absent ones included: isolation's rosters are then the
		// registry by construction, and an engine with nothing to seed or no
		// container story is a declaration it can read back, not a lookup
		// miss.
		isolation.RegisterCredentialSeed(d.Name, credentialSeedOf(&d))
		isolation.RegisterProvisioningPolicy(d.Name, d.Provisioning)
		isolation.RegisterEngineContainer(d.Name, d.Container, d.Distribution)
		if w, ok := d.InstanceConfig.Get(); ok {
			isolation.RegisterInstanceConfigWriter(d.Name, w(agent.SettingsOptions{}))
		}
	}
	return nil
}

// lookup resolves name to its descriptor by EXACT match on the registered
// name. No alias, case or prefix resolution: an engine has one spelling, and
// any other reaches the caller unresolved so it is refused rather than
// rounded to a real backend.
func lookup(name string) (*engine.Descriptor, bool) {
	d, ok := descriptors[name]
	return d, ok
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
	return DistributionFor(name) == coreengine.DistributionTestOnly
}

// DistributionFor returns the named engine's shipping policy, or Unset for a
// name nobody registered — which no policy check reads as any decision.
func DistributionFor(name string) coreengine.Distribution {
	d, ok := lookup(name)
	if !ok {
		return coreengine.DistributionUnset
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
	names := make([]string, 0, len(descriptors))
	for name := range descriptors {
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

// EnforcesReadOnlyPlan reports whether the named backend maps
// agent.PermissionPlan to a genuinely read-only, non-prompting mode (claude
// --permission-mode plan, for instance). A backend that doesn't would run
// plan unrestrained and can't be trusted to be headless-safe for it, so the
// run resolver collapses plan to default for it instead. An unregistered name
// reports false.
func EnforcesReadOnlyPlan(name string) bool {
	d, ok := lookup(name)
	return ok && d.EnforcesReadOnlyPlan
}

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
