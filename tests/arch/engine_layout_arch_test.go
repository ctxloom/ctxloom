//go:build arch

// Each engine package (internal/claude, internal/codex, internal/kiro,
// internal/opencode) knows how its OWN files are arranged: dir names like
// .codex/.claude/.kiro/.opencode, config file names, and the env vars a
// vendor CLI honors to relocate its home (CLAUDE_CONFIG_DIR, CODEX_HOME,
// KIRO_HOME, XDG_CONFIG_HOME/XDG_DATA_HOME). That knowledge used to be
// DUPLICATED as hand-typed string literals in four independently-maintained
// tables OUTSIDE the engine packages, with nothing to catch drift:
//
//   - internal/lm/isolation/auth.go's credentialSeedSpecs (env vars, dest
//     subdirs, host source-file paths for the credential seed).
//   - internal/lm/isolation/enginespec.go's per-engine overlayDirs and
//     transcriptStoreRel (the container-axis config-shadow and
//     transcript-mount tables).
//   - internal/lm/backends/mock.go's configHomeEnvKeys (the roster
//     isolation.EnvWorkspace threads into RunOptions.Env) — found STALE by
//     the census this gate encodes: it listed CLAUDE_CONFIG_DIR/CODEX_HOME/
//     KIRO_HOME but missed opencode's XDG_CONFIG_HOME/XDG_DATA_HOME despite
//     its own comment claiming to mirror EnvWorkspace. Fixed alongside this
//     gate (internal/lm/backends/mock.go now builds the roster from each
//     engine package's own exported env-var constant).
//   - internal/gitignore/gitignore.go's WorktreeArtifactPatterns (the LIVE
//     per-agent-worktree exclude set) and TransientArtifactPatterns/
//     WorktreeArtifactPatterns' pinned LEGACY .codex/* entries (the
//     pre-relocation project-root home, superseded by the per-session
//     instance (paths.SessionHomePath) but kept forever for a checkout that never
//     re-opens — see that file's own "THE .codex ENTRIES ARE NOW LEGACY"
//     comment).
//
// internal/lm/isolation and internal/gitignore cannot import
// claude/codex/kiro/opencode in PRODUCTION code to fix this at the source:
// every one of the four engine packages imports internal/acp (for its ACP
// chat driver), and internal/acp imports both internal/lm/isolation
// (container_transport.go) and internal/gitignore (verified by `go list
// -deps`) — so isolation/gitignore importing any engine package back would
// be a real cycle. internal/lm/backends is the one exception: it already
// imports all four engine packages directly (registry.go), with no cycle,
// so mock.go's roster now consumes their constants for real instead of
// re-typing them (see the file's own updated doc).
//
// Where the production cycle blocks direct consumption, THIS gate is the
// enforcement point instead: tests/arch is a standalone test binary free to
// import every package, so it cross-checks each table row against the owning
// engine package's own exported constant. A row that drifts — an isolation
// literal, an engine constant, or the two disagreeing — fails here with both
// values named.
//
// NOT every literal in these tables is a duplicated engine fact, and this
// gate does not pretend otherwise (a false-positive "drift" gate would be
// worse than the one it replaced):
//
//   - credentialSeedSpecs' destSubdir/HomeVars[].Subdir choose the LEAF NAME
//     isolation uses inside its OWN per-agent configHome tree. For claude
//     ("claude", no dot) and kiro/opencode ("kiro"/"xdg-config"/"xdg-data")
//     this is isolation's OWN arbitrary naming — it does not, and need not,
//     match the engine's ConfigDirName. codex is the sole DOCUMENTED
//     exception: homeVar's own doc says codex's Subdir is ".codex"
//     (dot-prefixed) SPECIFICALLY so codex's OWN cellScopedCodexHome join
//     lands on it — a real cross-package agreement, gated below. The other
//     three are escalated in this file's own report rather than force-gated
//     against a fact they do not actually share.
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
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/opencode"
)

// legacyCodexConfigFileName and legacyCodexAuthFileName are gitignore.go's
// OWN pinned pre-migration codex filenames (TransientArtifactPatterns /
// WorktreeArtifactPatterns' ".codex/config.toml" and ".codex/auth.json").
// Deliberately declared HERE rather than borrowed from codex.ConfigFileName/
// codex.AuthFileName — see this file's package doc.
const (
	legacyCodexConfigFileName = "config.toml"
	legacyCodexAuthFileName   = "auth.json"
)

// TestArch_EngineLayoutAgreement is the single gate for every table named in
// this file's package doc. Each sub-test below covers one table x one axis;
// a failure names the drifted row, the table it came from, and both values.
func TestArch_EngineLayoutAgreement(t *testing.T) {
	t.Run("credentialSeedSpecs_HomeVarEnvNames", testCredentialSeedHomeVarEnvNames)
	t.Run("credentialSeedSpecs_CodexDestSubdir", testCredentialSeedCodexDestSubdir)
	t.Run("credentialSeedSpecs_SourceFiles", testCredentialSeedSourceFiles)
	t.Run("spec_OverlayDirs", testSpecOverlayDirs)
	t.Run("spec_TranscriptStoreRel", testSpecTranscriptStoreRel)
	t.Run("mock_ConfigHomeEnvKeysRoster", testMockConfigHomeEnvKeysRoster)
	t.Run("gitignore_LivePatterns", testGitignoreLivePatterns)
	t.Run("gitignore_LegacyCodexPatterns", testGitignoreLegacyCodexPatterns)
}

// homeVarEnvCheck names one credentialSeedSpecs row's expected HomeVars env
// var names, in order, sourced from the owning engine package's own exported
// constant(s).
type homeVarEnvCheck struct {
	seedKey string
	want    []string
}

func testCredentialSeedHomeVarEnvNames(t *testing.T) {
	checks := []homeVarEnvCheck{
		{seedKey: "claude-code", want: []string{claude.ConfigDirEnv}},
		{seedKey: "codex", want: []string{codex.CodexHomeEnv}},
		{seedKey: "kiro", want: []string{kiro.HomeEnv, kiro.XDGDataHomeEnv}},
		{seedKey: "opencode", want: []string{opencode.XDGConfigHomeEnv, opencode.XDGDataHomeEnv}},
	}
	for _, c := range checks {
		t.Run(c.seedKey, func(t *testing.T) {
			hv := isolation.CredentialSeedHomeVars(c.seedKey)
			if hv == nil {
				t.Fatalf("isolation.CredentialSeedHomeVars(%q) returned nothing — credentialSeedSpecs is missing this row or its HomeVars", c.seedKey)
			}
			got := make([]string, len(hv))
			for i, v := range hv {
				got[i] = v.EnvVar
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("isolation.credentialSeedSpecs[%q].HomeVars env vars = %v, want %v (from the owning engine package's own exported env-var constant)",
					c.seedKey, got, c.want)
			}
		})
	}
}

// testCredentialSeedCodexDestSubdir gates the ONE destSubdir/Subdir pair
// homeVar's own doc documents as required to agree with the engine's
// ConfigDirName — see this file's package doc for why the other three
// engines' destSubdir is NOT gated the same way.
func testCredentialSeedCodexDestSubdir(t *testing.T) {
	destSubdir, ok := isolation.CredentialSeedDestSubdir("codex")
	if !ok {
		t.Fatal(`isolation.CredentialSeedDestSubdir("codex") reports no such row`)
	}
	if destSubdir != codex.ConfigDirName {
		t.Errorf("isolation.credentialSeedSpecs[\"codex\"].destSubdir = %q, want codex.ConfigDirName %q (codex's own cellScopedCodexHome join depends on this leaf name matching)",
			destSubdir, codex.ConfigDirName)
	}

	hv := isolation.CredentialSeedHomeVars("codex")
	if len(hv) != 1 {
		t.Fatalf(`isolation.CredentialSeedHomeVars("codex") = %v, want exactly one entry`, hv)
	}
	if hv[0].Subdir != codex.ConfigDirName {
		t.Errorf("isolation.credentialSeedSpecs[\"codex\"].HomeVars[0].Subdir = %q, want codex.ConfigDirName %q",
			hv[0].Subdir, codex.ConfigDirName)
	}
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
		{
			seedKey:  "codex",
			wantDir:  codex.ConfigDirName,
			wantDest: map[string]bool{codex.AuthFileName: true},
		},
		{
			seedKey: "opencode",
			// opencode's sourceFiles land under $HOME/.local/share/opencode —
			// ".local/share" is the generic XDG_DATA_HOME default (not
			// opencode-owned), "opencode" is opencode.DataDirName.
			wantDir:  filepath.ToSlash(filepath.Join(".local", "share", opencode.DataDirName)),
			wantDest: map[string]bool{opencode.AuthFileName: true, opencode.MCPAuthFileName: false},
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
		{backend: "kiro", want: kiro.ConfigDirName},
		{backend: "codex", want: codex.ConfigDirName},
		{backend: "opencode", want: opencode.ConfigDirName},
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
		{backend: "kiro", want: kiro.ConfigDirName},
		{backend: "codex", want: filepath.ToSlash(filepath.Join(codex.ConfigDirName, codex.SessionsDirName))},
		{backend: "opencode", want: filepath.ToSlash(filepath.Join(".local", "share", opencode.DataDirName))},
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

// testMockConfigHomeEnvKeysRoster pins backends.ConfigHomeEnvKeys() equal to
// the FULL, DEDUPLICATED set of env var names every credentialSeedSpecs
// engine's HomeVars names — the roster fix this gate was written to prove
// (mock's table used to omit opencode's XDG_CONFIG_HOME/XDG_DATA_HOME
// entirely).
func testMockConfigHomeEnvKeysRoster(t *testing.T) {
	want := map[string]bool{}
	for _, engine := range isolation.CredentialSeedEngineNames() {
		for _, hv := range isolation.CredentialSeedHomeVars(engine) {
			want[hv.EnvVar] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("no HomeVars env names found across any credentialSeedSpecs engine — the gate has nothing to compare against")
	}

	got := map[string]bool{}
	for _, k := range backends.ConfigHomeEnvKeys() {
		if got[k] {
			t.Errorf("backends.ConfigHomeEnvKeys() lists %q more than once", k)
		}
		got[k] = true
	}

	for k := range want {
		if !got[k] {
			t.Errorf("backends.ConfigHomeEnvKeys() is missing %q, present in isolation.credentialSeedSpecs' HomeVars", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("backends.ConfigHomeEnvKeys() lists %q, which no credentialSeedSpecs engine's HomeVars names — stale entry?", k)
		}
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
		{kiro.ConfigDirName + "/", "kiro.ConfigDirName"},
		{opencode.ConfigDirName + "/", "opencode.ConfigDirName"},
		{opencode.ConfigFileName, "opencode.ConfigFileName"},
		{codex.AgentsMDFile, "codex.AgentsMDFile"},
	}
	for _, w := range want {
		if !slices.Contains(patterns, w.pattern) {
			t.Errorf("internal/gitignore.WorktreeArtifactPatterns is missing %q (%s) — every ctxloom-written per-agent artifact must be excluded from a worktree merge-back",
				w.pattern, w.why)
		}
	}
}

// testGitignoreLegacyCodexPatterns checks the pinned-legacy .codex/* entries
// in BOTH TransientArtifactPatterns and WorktreeArtifactPatterns against this
// file's own locally-pinned legacy constants — see the package doc for why
// these are NOT compared against codex's live constants.
func testGitignoreLegacyCodexPatterns(t *testing.T) {
	legacyConfig := ".codex/" + legacyCodexConfigFileName
	legacyAuth := ".codex/" + legacyCodexAuthFileName

	for _, list := range []struct {
		name     string
		patterns []string
	}{
		{"TransientArtifactPatterns", gitignore.TransientArtifactPatterns},
		{"WorktreeArtifactPatterns", gitignore.WorktreeArtifactPatterns},
	} {
		if !slices.Contains(list.patterns, legacyConfig) {
			t.Errorf("internal/gitignore.%s is missing the pinned legacy entry %q", list.name, legacyConfig)
		}
		if !slices.Contains(list.patterns, legacyAuth) {
			t.Errorf("internal/gitignore.%s is missing the pinned legacy entry %q", list.name, legacyAuth)
		}
	}
}
