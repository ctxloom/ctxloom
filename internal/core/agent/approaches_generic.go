package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file holds the GENERIC approaches: implemented once, registered by any
// engine that can use them, imposed on none. They are here because more than
// one engine already uses the mechanism (the native context file;
// hook-carried context) — the test for lifting something into shared,
// not "might a future engine want it".

// NativeContextFile is the native context file every file engine has
// (claude's CLAUDE.md, the mock's context file) at rel beneath the project
// root: where it is presented.
//
// It is a Construct factory rather than a Construct because where the file
// lives is a STATIC fact about the engine, bound at registration.
func NativeContextFile(rel string) Construct {
	return func(SurfaceInputs, safefs.Root) Approach {
		return &nativeContextFile{rel: rel}
	}
}

type nativeContextFile struct {
	rel string
}

// Present declares the native file beneath the advised project root. No flag:
// an engine started in this dir finds it by name.
func (c *nativeContextFile) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(c.rel).Build()
}

// HookCarriedContext is context that reaches the engine at RUN TIME through a
// SessionStart inject-context hook reading a content-addressed cache file.
// It writes NOTHING of its own: the hook rides the settings surface, whose
// writer emits hook registrations, and the launch installs the cache file and
// the hook entry once it sees this approach resolved for the context surface — on EVERY cell, so a
// worktree or container launch pinned to it gets its context exactly as a
// shared one does. At rest (apply) the hook is assembled into the settings
// payload by the caller, so this delivery is the documented no-op there too.
//
// Present names the cache file the hook will read, computed from the run's
// fragments the same way WriteContextFile names it — the leaf is
// content-derived, which is exactly why this is knowable only on a
// CONSTRUCTED approach.
func HookCarriedContext(in SurfaceInputs, _ safefs.Root) Approach {
	return hookCarriedContext{fragments: in.Fragments}
}

type hookCarriedContext struct {
	fragments []*Fragment
}

// Present names the content-addressed cache file the hook reads; a run with
// no context names nothing.
func (h hookCarriedContext) Present(start present.Start) present.Presentation {
	content := assembleDedupedContext(h.fragments)
	if content == "" {
		return present.Presentation{}
	}
	sum := sha256.Sum256([]byte(content))
	leaf := hex.EncodeToString(sum[:8]) + ".md"
	return start.UnderProjectRoot(filepath.Join(SCMContextSubdir, leaf)).Build()
}

var (
	_ Approach = (*nativeContextFile)(nil)
	_ Approach = hookCarriedContext{}
)
