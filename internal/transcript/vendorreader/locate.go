package vendorreader

import (
	"time"
)

// Located is one engine transcript found on disk that ctxloom did not create.
//
// It is deliberately thin: a Locator answers WHERE and WHOSE, never WHAT. The
// content is the existing per-engine adapters' business, selected by recorded
// engine version, and nothing here should tempt a caller to parse for itself.
type Located struct {
	// Engine is the backend name, matching the engine identifiers used
	// everywhere else ("claude-code", "codex", "kiro").
	Engine string
	// SessionID is the engine-native session identifier.
	SessionID string
	// Path is the transcript file.
	Path string
	// WorkDir is the project the ENGINE recorded for this session, read out of
	// the transcript rather than inferred from where the file sits. Every
	// format measured records it, and a directory name that encodes a path is
	// a lossy encoding of the same fact.
	WorkDir string
	// ModifiedAt is the file's mtime, the only ordering signal available
	// without reading the whole transcript.
	ModifiedAt time.Time
}

// Locator enumerates an engine's own transcript store.
//
// # Why this exists at all, given the scrapers that were deleted
//
// ctxloom learns a transcript's path at BIND time, from the runner, so it can
// only list sessions it brokered itself. Four per-engine scrapers used to walk
// the vendors' stores and were retired for guessing at private layouts — kiro's
// was confirmed broken when its storage became a SQLite blob, and it reported
// NO sessions rather than an error.
//
// That failure mode is the whole contract here. A Locator MUST refuse when it
// finds a store it does not recognise, and must never report emptiness it has
// not established: "you have no history" and "I no longer understand this
// store" are different answers and only one of them is safe to act on.
//
// An ABSENT store is not a refusal — the engine simply is not installed, which
// is legitimately empty.
type Locator interface {
	// Discover returns every transcript this engine holds for workDir. An
	// empty workDir means every project the store knows.
	//
	// It returns an error when the store exists but does not have the shape
	// this Locator understands. It returns an empty slice and no error when
	// the store is absent, or present and genuinely holds nothing.
	Discover(workDir string) ([]Located, error)
}

// UnrecognizedStoreError reports that an engine's transcript store exists but
// does not look the way this Locator expects.
//
// It carries what was looked for and what was found, because the diagnosis a
// user needs is exactly that comparison — and because this error IS the bug
// report that gets the Locator updated when a vendor moves its storage.
type UnrecognizedStoreError struct {
	Engine   string
	Root     string
	Expected string
	Found    string
}

func (e *UnrecognizedStoreError) Error() string {
	return e.Engine + ": the transcript store at " + e.Root +
		" is not in a layout ctxloom recognises (expected " + e.Expected +
		"; found " + e.Found +
		") — refusing to report zero sessions, which is indistinguishable from having none"
}
