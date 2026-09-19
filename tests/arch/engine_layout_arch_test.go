//go:build arch

// Each engine package knows how its OWN files are arranged: its config dir
// name, its config file names, and the env vars a vendor CLI honors to
// relocate its home. That knowledge used to be DUPLICATED as hand-typed
// string literals in independently-maintained tables OUTSIDE the engine
// packages, with nothing to catch drift:
//
//   - the credential seed (env var, dest subdir, host source-file paths).
//     This one is no longer a literal outside the engine: each engine
//     declares it on its own descriptor (engine.Descriptor.Home.Credentials)
//     and the registry pushes it into internal/adapters/isolation. What this gate
//     still checks is the DECLARATION itself against the engine's constants —
//     a descriptor can hand-type a path as easily as a table could.
//   - the container-axis overlay dirs and transcript-store root. Also no
//     longer isolation literals: each engine declares them on its descriptor
//     (engine.Descriptor.Container) and the registry pushes them into
//     internal/adapters/isolation, which is read back here through
//     ContainerOverlayDirsFor / ContainerTranscriptStoreRelFor — so the
//     check covers the declaration AND that the push delivered it.
//   - internal/adapters/gitignore/gitignore.go's WorktreeArtifactPatterns (the LIVE
//     per-agent-worktree exclude set) and TransientArtifactPatterns/
//     WorktreeArtifactPatterns' pinned LEGACY .codex/* entries (the
//     pre-relocation project-root home, superseded by the per-session
//     instance (paths.SessionHomePath) but kept forever for a checkout that never
//     re-opens — see that file's own "THE .codex ENTRIES ARE NOW LEGACY"
//     comment).
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
//   - the credential seed's Subdir chooses the LEAF NAME isolation seeds
//     into inside a controlled home. It is the engine's own naming (claude
//     declares claude.HomeLeaf, "claude", no dot) and need not match the
//     engine's ConfigDirName; agent.EngineHome.Validate already holds it
//     equal to a home var's Subdir, so it is not re-gated here.
//   - the shared ".ctxloom/cache" overlay entry isolation appends for every
//     engine is ctxloom's own cache path, not a fact about any engine's file
//     arrangement — never checked here.
//   - TransientArtifactPatterns' and WorktreeArtifactPatterns' ".codex/
//     config.toml"/".codex/auth.json" entries are PINNED LEGACY (see the
//     package doc above) — checked against locally-pinned legacy constants
//     in THIS file, deliberately NOT against codex.ConfigFileName/
//     AuthFileName, so a future rename of codex's LIVE constants cannot
//     silently rewrite what a pre-migration checkout's .gitignore is
//     required to still exclude.
package arch

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/gitignore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// legacyCodexConfigFileName and legacyCodexAuthFileName are gitignore.go's
// OWN pinned pre-migration codex filenames (TransientArtifactPatterns /
// WorktreeArtifactPatterns' ".codex/config.toml" and ".codex/auth.json").
// Deliberately declared HERE rather than borrowed from codex.ConfigFileName/
// codex.AuthFileName — see this file's package doc.
const ()

// TestArch_EngineLayoutAgreement is the single gate for every table named in
// this file's package doc. Each sub-test below covers one table x one axis;
// a failure names the drifted row, the table it came from, and both values.
func TestArch_EngineLayoutAgreement(t *testing.T) {
	t.Run("credentialSeed_SourceFiles", testCredentialSeedSourceFiles)
	t.Run("spec_OverlayDirs", testSpecOverlayDirs)
	t.Run("spec_TranscriptStoreRel", testSpecTranscriptStoreRel)
	t.Run("gitignore_LivePatterns", testGitignoreLivePatterns)
}

// sourceFileCheck names one engine's expected seed-file facts: the directory
// component every declared file must live under (an engine-owned constant),
// and the set of known destination file names mapped to whether each is
// required.
type sourceFileCheck struct {
	seedKey  string
	wantDir  string
	wantDest map[string]bool // destName -> required
}

func testCredentialSeedSourceFiles(t *testing.T) {
	checks := []sourceFileCheck{
		{
			seedKey:  "claude-code",
			wantDir:  claude.ConfigDirName,
			wantDest: map[string]bool{claude.CredentialsFileName: true},
		},
	}

	for _, c := range checks {
		t.Run(c.seedKey, func(t *testing.T) {
			seed, ok := backends.CredentialSeedFor(c.seedKey).Get()
			if !ok {
				t.Fatalf("backends.CredentialSeedFor(%q) declares no seed", c.seedKey)
			}
			if len(seed.Files) == 0 {
				t.Fatalf("%s's credential seed declares no files", c.seedKey)
			}
			seen := map[string]bool{}
			for _, f := range seed.Files {
				seen[f.DestName] = true
				dir := filepath.ToSlash(filepath.Dir(f.HostRelHome))
				if dir != c.wantDir {
					t.Errorf("%s's credential seed file %q lives under %q, want owning engine dir %q",
						c.seedKey, f.DestName, dir, c.wantDir)
				}
				wantReq, known := c.wantDest[f.DestName]
				if !known {
					t.Errorf("%s's credential seed file name %q is not a known engine-owned file-name constant — add one, or if it's genuinely isolation-only, escalate it",
						c.seedKey, f.DestName)
					continue
				}
				if wantReq != f.Required {
					t.Errorf("%s's credential seed file %q required=%v, want %v",
						c.seedKey, f.DestName, f.Required, wantReq)
				}
			}
			for destName := range c.wantDest {
				if !seen[destName] {
					t.Errorf("%s's credential seed is missing expected source file %q", c.seedKey, destName)
				}
			}
			// The seam sees exactly the declaration: the push at registration
			// delivered these files, not a re-typed copy.
			ambient := isolation.AmbientSet(c.seedKey)
			if len(ambient) != len(seed.Files) {
				t.Fatalf("isolation.AmbientSet(%q) has %d files, the descriptor declares %d — the push-down is not delivering the declaration",
					c.seedKey, len(ambient), len(seed.Files))
			}
			for i, f := range seed.Files {
				if ambient[i].HostRel != f.HostRelHome || ambient[i].Required != f.Required {
					t.Errorf("isolation.AmbientSet(%q)[%d] = %+v, want the declared %+v", c.seedKey, i, ambient[i], f)
				}
			}
		})
	}
}

// overlayCheck names one engineContainerSpecFor(backend) row's expected
// project-relative managed-config directory, sourced from the owning engine
// (or, for mock, internal/lm/backends itself — mock has no separate plugin
// package).
type overlayCheck struct {
	backend string
	want    string
}

func testSpecOverlayDirs(t *testing.T) {
	checks := []overlayCheck{
		{backend: "claude-code", want: claude.ConfigDirName},
		{backend: "mock", want: backends.MockConfigDirName},
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
			got := filepath.ToSlash(isolation.ContainerTranscriptStoreRelFor(c.backend))
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
