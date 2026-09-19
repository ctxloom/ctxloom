package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// This file holds the GENERIC approaches: implemented once, registered by any
// engine that can use them, imposed on none. They are here because more than
// one engine already uses the mechanism (the native managed-section context
// file; hook-carried context) — the test for lifting something into shared,
// not "might a future engine want it".

// NativeContextFile is the native managed-section context file every file
// engine has (claude's CLAUDE.md, the mock's context file): the engine's own
// ContextWriter merges the run's context into the file at rel beneath the
// project root, and the handle strips the managed section back out.
//
// It is a Construct factory rather than a Construct because the things that
// vary per engine — the writer, where the file lives, and the label the
// shared-cwd warning names it by — are STATIC facts about the engine, bound at
// registration, while the content is a per-run fact bound at construction.
func NativeContextFile(name, rel string, writer func(fs afero.Fs) ContextWriter) Construct {
	return func(in SurfaceInputs, fs afero.Fs) Approach {
		fs = GetFS(fs)
		return &nativeContextFile{name: name, rel: rel, fs: fs, writer: writer(fs), content: in.Context}
	}
}

type nativeContextFile struct {
	name    string
	rel     string
	fs      afero.Fs
	writer  ContextWriter
	content string
}

// UnsafeInfo names this surface for the shared-cwd fallback's warning.
func (c *nativeContextFile) UnsafeInfo() string { return c.name }

// State implements StateReader: what the native file currently carries in its
// managed section, read through the same marker core the write side merges
// through, so the two cannot disagree about where the section lives. An absent
// file or an absent section reports Found/HasSection false; Currency turns
// that into the missing verdict.
func (c *nativeContextFile) State(dir string) (DeliveryState, error) {
	state, err := ReadManagedContext(c.fs, filepath.Join(dir, c.rel), c.rel)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// Present declares the native file beneath the advised project root. No flag:
// an engine started in this dir finds it by name.
func (c *nativeContextFile) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(c.rel).Build()
}

// Deliver merges the context into the native file via the engine's
// ContextWriter and returns a handle whose Cleanup strips the managed section
// — the honest reversal of a MARKER-MERGED write.
func (c *nativeContextFile) Deliver(start present.Start) (Delivered, error) {
	return DeliverManagedContext(c.writer, start.Paths().ProjectRoot.Host, c.content)
}

// HookCarriedContext is context that reaches the engine at RUN TIME through a
// SessionStart inject-context hook reading a content-addressed cache file.
// It writes NOTHING of its own: the hook rides the settings/hooks surface
// (Rides), and the launch installs the cache file and the hook entry once it
// sees this approach resolved for the context surface — on EVERY cell, so a
// worktree or container launch pinned to it gets its context exactly as a
// shared one does. At rest (apply) the hook is assembled into the settings
// payload by the caller, so this delivery is the documented no-op there too.
//
// Present names the cache file the hook will read, computed from the run's
// fragments the same way WriteContextFile names it — the leaf is
// content-derived, which is exactly why this is knowable only on a
// CONSTRUCTED approach.
func HookCarriedContext(in SurfaceInputs, _ afero.Fs) Approach {
	return hookCarriedContext{fragments: in.Fragments}
}

type hookCarriedContext struct {
	fragments []*Fragment
}

// Rides reports the surface the injection hook is written into.
func (hookCarriedContext) Rides() SurfaceKind { return SurfaceSettings }

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

// Deliver writes nothing and returns a nil handle: the caller's
// nil-handle-skip convention treats this exactly like any other no-op
// delivery. Writing a native file here too would DOUBLE the context.
func (hookCarriedContext) Deliver(present.Start) (Delivered, error) { return nil, nil }

var (
	_ Approach    = (*nativeContextFile)(nil)
	_ StateReader = (*nativeContextFile)(nil)
	_ Approach    = hookCarriedContext{}
	_ Rider       = hookCarriedContext{}
)
