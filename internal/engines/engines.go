// Package engines is the COMPOSITION ROOT for the engines ctxloom ships: the
// production list that names engine packages. Adding an engine is creating
// its package, writing its constructor there, and adding it to this list —
// no shared table anywhere learns its name.
//
// Build composes the engine KINDS into an engine.Registry value: each
// package's constructor has already validated its Definition (the ONE place
// an incoherent declaration is refused), so the registry only refuses a
// duplicate name. Compose is the process-wide composition: idempotent, so a
// test binary and a CLI that both compose see one registry (Registry).
package engines

import (
	"sync"

	mockreader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/mock"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	claudeengine "github.com/ctxloom/ctxloom/internal/engines/claude/engine"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// Build composes every shipped engine kind into a Registry: the mock kind
// with its doubles, and claude — each handed the readers of its own
// transcript store here, at the root, because the readers are transcript
// adapters an engine package must not import; claude its version reading
// for the same reason (adapters/engineversion).
func Build() (engine.Registry, error) {
	c, err := claude.Build(claude.WithTranscripts(claudeengine.Transcripts()...), claude.WithVersion(claudeengine.Version()))
	if err != nil {
		return engine.Registry{}, err
	}
	return engine.NewRegistry(append(mock.Doubles(mock.WithTranscripts(mockTranscripts()...)), c)...)
}

// mockTranscripts are the mock's degenerate readers as engine.TranscriptReader
// values: a single-entry reader registry cannot fail, and mock is what
// proves the selection has no branch to take wrongly.
func mockTranscripts() []engine.TranscriptReader {
	out := make([]engine.TranscriptReader, 0, len(mockreader.VersionedAdapters))
	for _, a := range mockreader.VersionedAdapters {
		out = append(out, a)
	}
	return out
}

var (
	mu       sync.Mutex
	composed bool
	registry engine.Registry
	err      error
)

// Compose is the process-wide composition: the first call does Build's work
// and later calls return its result, so a TestMain and a CLI that both
// compose see one registry.
func Compose() error {
	mu.Lock()
	defer mu.Unlock()
	if !composed {
		registry, err = Build()
		composed = true
	}
	return err
}

// MustCompose is Compose for a TestMain, where a composition failure has no
// caller to return to and a panic is the correct loud failure.
func MustCompose() {
	if err := Compose(); err != nil {
		panic("engines: " + err.Error())
	}
}

// Registry is the composed registry — what every adapter that resolves an
// engine by name reads. It composes on first use; a composition failure is
// a programming error in a shipped engine's declaration, so it panics.
func Registry() engine.Registry {
	MustCompose()
	mu.Lock()
	defer mu.Unlock()
	return registry
}

// Use swaps the composed registry for reg until restore is called: a test's
// seam for standing a synthetic engine in front of the adapters that read
// Registry. Production composes once and never calls it.
func Use(reg engine.Registry) (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	prev, prevComposed, prevErr := registry, composed, err
	registry, composed, err = reg, true, nil
	return func() {
		mu.Lock()
		defer mu.Unlock()
		registry, composed, err = prev, prevComposed, prevErr
	}
}

// NamesWhere lists, sorted, the composed engines keep accepts
// (engine.Registry.NamesWhere over the process registry).
func NamesWhere(keep func(name string, e engine.Engine) bool) []string {
	var out []string
	for _, n := range Registry().NamesWhere(func(n engine.Name, e engine.Engine) bool { return keep(string(n), e) }) {
		out = append(out, string(n))
	}
	return out
}

// Hosted is agent.HostedIn over the process registry. Every shipped kind is
// Hosted (TestBuild_EveryShippedEngineIsHosted).
func Hosted(name string) (agent.Hosted, bool) { return agent.HostedIn(Registry(), name) }

// EngineCLIs is agent.EngineCLIsIn over the process registry.
func EngineCLIs(name string) ([]agent.EngineCLI, bool) { return agent.EngineCLIsIn(Registry(), name) }
