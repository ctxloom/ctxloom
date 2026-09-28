//go:build arch

// Each engine package knows how its OWN files are arranged: its config dir
// name, its config file names, and the env vars a vendor CLI honors to
// relocate its home. That knowledge used to be DUPLICATED as hand-typed
// string literals in independently-maintained tables OUTSIDE the engine
// packages, with nothing to catch drift:
//
//   - the container-axis overlay dirs and transcript-store root. Also no
//     longer isolation literals: each engine declares them on its descriptor
//     (engine.Descriptor.Container) and the registry pushes them into
//     internal/adapters/isolation, which is read back here through
//     ContainerOverlayDirsFor / ContainerTranscriptStoreRelFor — so the
//     check covers the declaration AND that the push delivered it.
//   - internal/adapters/gitignore/gitignore.go's WorktreeArtifactPatterns (the LIVE
//     per-agent-worktree exclude set).
//
// internal/adapters/gitignore still carries its facts as literals rather than
// importing the engine packages, so nothing in PRODUCTION code makes the two
// sides agree there. The isolation-side facts are now DECLARED by each
// engine from its own constants; what remains checkable is that the
// declaration was built from them rather than re-typed.
//
// Everywhere the literals remain, THIS gate is the enforcement point:
// tests/arch is a standalone test binary free to import every package, so it
// cross-checks each table row against the owning engine package's own
// exported constant. A row that drifts — an isolation
// literal, an engine constant, or the two disagreeing — fails here with both
// values named.
//
// NOT every literal in these tables is a duplicated engine fact, and this
// gate does not pretend otherwise (a false-positive "drift" gate would be
// worse than the one it replaced):
//
//   - the shared ".ctxloom/cache" overlay entry isolation appends for every
//     engine is ctxloom's own cache path, not a fact about any engine's file
//     arrangement — never checked here.
package arch

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/gitignore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestArch_EngineLayoutAgreement is the single gate for every table named in
// this file's package doc. Each sub-test below covers one table x one axis;
// a failure names the drifted row, the table it came from, and both values.
func TestArch_EngineLayoutAgreement(t *testing.T) {
	t.Run("spec_OverlayDirs", testSpecOverlayDirs)
	t.Run("spec_TranscriptStoreRel", testSpecTranscriptStoreRel)
	t.Run("gitignore_LivePatterns", testGitignoreLivePatterns)
}

// overlayCheck names one engineContainerSpecFor(backend) row's expected
// project-relative managed-config directory, sourced from the owning engine
// (for mock, internal/engines/mock).
type overlayCheck struct {
	backend string
	want    string
}

func testSpecOverlayDirs(t *testing.T) {
	checks := []overlayCheck{
		{backend: "claude-code", want: claude.ConfigDirName},
		{backend: "mock", want: mock.MockConfigDirName},
	}
	for _, c := range checks {
		t.Run(c.backend, func(t *testing.T) {
			dirs := isolation.ContainerOverlayDirsFor(c.backend)
			if !slices.Contains(dirs, c.want) {
				t.Errorf("isolation spec overlayDirs for backend %q = %v, missing owning engine dir %q",
					c.backend, dirs, c.want)
			}
		})
	}
}

type transcriptCheck struct {
	backend string
	want    string
}

func testSpecTranscriptStoreRel(t *testing.T) {
	checks := []transcriptCheck{
		{backend: "claude-code", want: filepath.ToSlash(filepath.Join(claude.ConfigDirName, claude.TranscriptsDirName))},
	}
	for _, c := range checks {
		t.Run(c.backend, func(t *testing.T) {
			got := isolation.ContainerTranscriptStoreRelFor(c.backend)
			if got != c.want {
				t.Errorf("isolation spec transcriptStoreRel for backend %q = %q, want %q",
					c.backend, got, c.want)
			}
		})
	}
}

// testGitignoreLivePatterns checks WorktreeArtifactPatterns' LIVE
// (non-legacy) engine-owned entries against each owning engine package's own
// constant.
func testGitignoreLivePatterns(t *testing.T) {
	patterns := gitignore.WorktreeArtifactPatterns

	type wantPattern struct {
		pattern string
		why     string
	}
	want := []wantPattern{
		{claude.ConfigDirName + "/", "claude.ConfigDirName"},
		{claude.MCPFileName, "claude.MCPFileName"},
		{claude.ContextFileName, "claude.ContextFileName"},
	}
	for _, w := range want {
		if !slices.Contains(patterns, w.pattern) {
			t.Errorf("internal/adapters/gitignore.WorktreeArtifactPatterns is missing %q (%s) — every ctxloom-written per-agent artifact must be excluded from a worktree merge-back",
				w.pattern, w.why)
		}
	}
}
