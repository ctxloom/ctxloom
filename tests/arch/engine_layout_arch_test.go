//go:build arch

// Each engine package knows how its OWN files are arranged: its config dir
// name, its config file names, and the env vars a vendor CLI honors to
// relocate its home. That knowledge used to be DUPLICATED as hand-typed
// string literals in independently-maintained tables OUTSIDE the engine
// packages, with nothing to catch drift:
//
//   - internal/lm/isolation/auth.go's credentialSeedSpecs (env vars, dest
//     subdirs, host source-file paths for the credential seed).
//   - internal/lm/isolation/enginespec.go's per-engine overlayDirs and
//     transcriptStoreRel (the container-axis config-shadow and
//     transcript-mount tables).
//   - internal/gitignore/gitignore.go's WorktreeArtifactPatterns (the LIVE
//     per-agent-worktree exclude set) and TransientArtifactPatterns/
//     WorktreeArtifactPatterns' pinned LEGACY .codex/* entries (the
//     pre-relocation project-root home, superseded by the per-session
//     instance (paths.SessionHomePath) but kept forever for a checkout that never
//     re-opens — see that file's own "THE .codex ENTRIES ARE NOW LEGACY"
//     comment).
//
// internal/lm/isolation and internal/gitignore still carry these facts as
// literals rather than importing the engine packages, so nothing in
// PRODUCTION code makes the two sides agree. internal/lm/backends is the
// exception: it already imports the engine packages directly (registry.go),
// so mock.go's roster consumes their constants for real instead of re-typing
// them (see that file's own doc).
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
//   - credentialSeedSpecs' destSubdir chooses the LEAF NAME isolation seeds
//     into inside a controlled home. For claude
//     ("claude", no dot) and opencode ("xdg-config"/"xdg-data") this is
//     isolation's OWN arbitrary naming — it does not, and need not, match
//     the engine's ConfigDirName. codex is the sole DOCUMENTED exception:
//     homeVar's own doc says codex's Subdir is ".codex" (dot-prefixed)
//     SPECIFICALLY so codex's OWN cellScopedCodexHome join lands on it — a
//     real cross-package agreement, gated below. The rest are escalated in
//     this file's own report rather than force-gated against a fact they do
//     not actually share.
//   - the shared ".ctxloom/cache" overlay entry every spec carries is
//     ctxloom's own cache path, not a fact about any engine's file
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

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
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
	t.Run("credentialSeedSpecs_SourceFiles", testCredentialSeedSourceFiles)
	t.Run("spec_OverlayDirs", testSpecOverlayDirs)
	t.Run("spec_TranscriptStoreRel", testSpecTranscriptStoreRel)
	t.Run("gitignore_LivePatterns", testGitignoreLivePatterns)
}

// sourceFileCheck names one credentialSeedSpecs row's expected seed-file
// facts: the directory component every listed file must live under (an
// engine-owned constant), and the set of known destination file names mapped
// to whether each is required.
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
			files := isolation.CredentialSeedSourceFiles(c.seedKey)
			if len(files) == 0 {
				t.Fatalf("isolation.CredentialSeedSourceFiles(%q) returned nothing", c.seedKey)
			}
			seen := map[string]bool{}
			for _, f := range files {
				seen[f.DestName] = true
				dir := filepath.ToSlash(filepath.Dir(f.HostRelToHome))
				if dir != c.wantDir {
					t.Errorf("isolation.credentialSeedSpecs[%q] source file %q lives under %q, want owning engine dir %q",
						c.seedKey, f.DestName, dir, c.wantDir)
				}
				wantReq, known := c.wantDest[f.DestName]
				if !known {
					t.Errorf("isolation.credentialSeedSpecs[%q] source file name %q is not a known engine-owned file-name constant — add one, or if it's genuinely isolation-only, escalate it",
						c.seedKey, f.DestName)
					continue
				}
				if wantReq != f.Required {
					t.Errorf("isolation.credentialSeedSpecs[%q] source file %q required=%v, want %v",
						c.seedKey, f.DestName, f.Required, wantReq)
				}
			}
			for destName := range c.wantDest {
				if !seen[destName] {
					t.Errorf("isolation.credentialSeedSpecs[%q] is missing expected source file %q", c.seedKey, destName)
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
			t.Errorf("internal/gitignore.WorktreeArtifactPatterns is missing %q (%s) — every ctxloom-written per-agent artifact must be excluded from a worktree merge-back",
				w.pattern, w.why)
		}
	}
}
