// Package engines is the COMPOSITION ROOT for the engines ctxloom ships: the
// production list that names engine packages. Adding an engine is creating
// its package, writing its constructor there, and adding it to this list —
// no shared table anywhere learns its name.
//
// Build composes the engine KINDS into an engine.Registry value: each
// package's constructor has already validated its Definition (the ONE place
// an incoherent declaration is refused), so the registry only refuses a
// duplicate name. Register installs the same engines' hosting records into
// the backend registry that still runs today's launches; it is idempotent
// per process so a test binary and a CLI that both compose see one
// registration.
package engines

import (
	"sync"

	mockreader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/mock"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	claudeengine "github.com/ctxloom/ctxloom/internal/engines/claude/engine"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// Build composes every shipped engine kind into a Registry: the mock kind
// with its doubles, and claude — each handed the readers of its own
// transcript store here, at the root, because the readers are transcript
// adapters an engine package must not import.
func Build() (engine.Registry, error) {
	c, err := claude.Build(claude.WithTranscripts(claudeengine.Transcripts()...))
	if err != nil {
		return engine.Registry{}, err
	}
	return engine.NewRegistry(append(mock.Doubles(mock.WithTranscripts(mockTranscripts()...)), c)...)
}

// mockTranscripts are the mock's degenerate readers as engine.TranscriptReader
// values: a single-entry reader registry cannot fail, and mock is what proves
// the selection has no branch to take wrongly.
func mockTranscripts() []engine.TranscriptReader {
	out := make([]engine.TranscriptReader, 0, len(mockreader.VersionedAdapters))
	for _, a := range mockreader.VersionedAdapters {
		out = append(out, a)
	}
	return out
}

var (
	once sync.Once
	err  error
)

// Register composes every shipped engine into the backend registry: the
// kinds from Build, each paired with its hosting record. It is idempotent
// per process: the first call does the work and later calls return its
// result.
func Register() error {
	once.Do(func() {
		var reg engine.Registry
		if reg, err = Build(); err != nil {
			return
		}
		err = backends.Register(reg, append(backends.MockHostings(), claudeengine.Hosting())...)
	})
	return err
}

// MustRegister is Register for a TestMain, where a composition failure has
// no caller to return to and a panic is the correct loud failure.
func MustRegister() {
	if err := Register(); err != nil {
		panic("engines: " + err.Error())
	}
}
