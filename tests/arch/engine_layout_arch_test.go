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
//   - the per-agent-worktree exclude set (the paths an engine writes into a
//     working tree). Also no longer gitignore literals: each engine declares
//     them (engine.Definition.ProjectArtifacts), and
//     isolation.WorktreeArtifactPatterns unions ctxloom's own
//     (gitignore.CtxloomWorktreePatterns) with every registered engine's
//     through the registry — read back here, and the gitignore package is
//     held to spelling none of an engine's paths itself.
//
// The facts are DECLARED by each engine from its own constants; what remains
// checkable is that the declaration was built from them rather than re-typed,
// and that the consumer reads the declaration rather than a copy.
//
// THIS gate is the enforcement point:
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
	"go/token"
	"slices"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/gitignore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestArch_EngineLayoutAgreement is the single gate for every table named in
// this file's package doc. Each sub-test below covers one table x one axis;
// a failure names the drifted row, the table it came from, and both values.
func TestArch_EngineLayoutAgreement(t *testing.T) {
	t.Run("spec_OverlayDirs", testSpecOverlayDirs)
	t.Run("gitignore_LivePatterns", testGitignoreLivePatterns)
	t.Run("gitignore_SpellsNoEnginePath", testGitignoreSpellsNoEnginePath)
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
		{backend: "mock", want: mock.ConfigDirName},
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

// testGitignoreLivePatterns checks the LIVE per-agent-worktree exclude set is
// exactly ctxloom's own patterns plus the union of every registered engine's
// declared ProjectArtifacts, and that each engine's declaration carries the
// paths its own constants name — so the set is derived, and the derivation
// starts from the engine's facts rather than a copy of them.
func testGitignoreLivePatterns(t *testing.T) {
	reg := engines.Registry()
	want := slices.Clone(gitignore.CtxloomWorktreePatterns)
	for _, n := range reg.Names(nil) {
		e, _ := reg.Lookup(n)
		for _, p := range e.Root().ProjectArtifacts {
			if !slices.Contains(want, p) {
				want = append(want, p)
			}
		}
	}
	got := isolation.WorktreeArtifactPatterns()
	slices.Sort(want)
	sorted := slices.Sorted(slices.Values(got))
	if !slices.Equal(want, sorted) {
		t.Errorf("isolation.WorktreeArtifactPatterns() = %v, want ctxloom's own plus every registered engine's declared ProjectArtifacts = %v", sorted, want)
	}

	declared := map[engine.Name][]struct{ pattern, why string }{
		engine.Name(claude.EngineName): {
			{claude.ConfigDirName + "/", "claude.ConfigDirName"},
			{claude.MCPFileName, "claude.MCPFileName"},
			{claude.ContextFileName, "claude.ContextFileName"},
		},
		mock.Name: {
			{mock.ConfigDirName + "/", "mock.ConfigDirName"},
			{mock.ContextFileName, "mock.ContextFileName"},
		},
	}
	for name, wants := range declared {
		e, ok := reg.Lookup(name)
		if !ok {
			t.Errorf("engine %q is not registered", name)
			continue
		}
		for _, w := range wants {
			if !slices.Contains(e.Root().ProjectArtifacts, w.pattern) {
				t.Errorf("%s declares ProjectArtifacts %v, missing %q (%s) — every per-agent artifact it writes must be excluded from a worktree merge-back",
					name, e.Root().ProjectArtifacts, w.pattern, w.why)
			}
		}
	}
}

// testGitignoreSpellsNoEnginePath holds internal/adapters/gitignore to naming
// no engine's working-tree path itself: a string constant equal to any
// registered engine's declared ProjectArtifacts entry (with or without its
// trailing slash) is a copy of the engine's fact that will drift from it. The
// same folding as the engine-identity gate, so a constant split or renamed
// is still seen.
func testGitignoreSpellsNoEnginePath(t *testing.T) {
	paths := map[string]bool{}
	reg := engines.Registry()
	for _, n := range reg.Names(nil) {
		e, _ := reg.Lookup(n)
		for _, p := range e.Root().ProjectArtifacts {
			paths[strings.ToLower(p)] = true
			paths[strings.ToLower(strings.TrimSuffix(p, "/"))] = true
		}
	}
	if len(paths) == 0 {
		t.Fatal("no registered engine declares ProjectArtifacts — the gate has nothing to refuse")
	}
	fset := token.NewFileSet()
	files := parseProductionFiles(t, fset)
	consts := packageConsts(files)
	const dir = "internal/adapters/gitignore"
	checked := 0
	for _, f := range files {
		if f.dir != dir {
			continue
		}
		checked++
		for _, v := range identityViolations(fset, f, consts[f.dir], paths) {
			t.Errorf("%s — an engine's working-tree paths are its declaration (engine.Definition.ProjectArtifacts), read through the registry, never a gitignore literal", v)
		}
	}
	if checked == 0 {
		t.Fatalf("no production file under %s — the gate checked nothing", dir)
	}
}
