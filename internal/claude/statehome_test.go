package claude

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestStateHome_IsTheProjectScopedEngineHome pins claude's project-scoped
// engine home to the ONE location the engine-home policy names
// (paths.EngineStateHome), keyed by claude's REGISTERED backend name — the
// same key credentialSeedSpecs and the backends registry use, so a second
// engine can never land in claude's directory.
func TestStateHome_IsTheProjectScopedEngineHome(t *testing.T) {
	workDir := filepath.Join(string(filepath.Separator), "proj")
	want := paths.EngineStateHome(filepath.Join(workDir, paths.AppDirName), "claude-code")
	if got := StateHome(workDir); got != want {
		t.Errorf("StateHome(%q) = %q, want %q", workDir, got, want)
	}
}

// TestInTreeConfigDir_IsTheSeedDestination is the writer-agreement pin: the
// value ctxloom hands claude as CLAUDE_CONFIG_DIR on an in-tree agent run and
// the directory isolation's credential seed writes .credentials.json into MUST
// be the same directory, or the engine reads a home nothing seeded. Both are
// derived here from the ONE helper plus the ONE registry, never from a literal
// spelled twice.
//
// A future static writer (phase 2's home-keyed context delivery) resolves
// through InTreeConfigDir too — this test is what makes that inherit the
// discipline instead of re-deriving the join by hand, exactly as
// TestCodexHome_RunPathAndStaticWritersAgree does for codex.
func TestInTreeConfigDir_IsTheSeedDestination(t *testing.T) {
	workDir := filepath.Join(string(filepath.Separator), "proj")
	destSubdir, ok := isolation.CredentialSeedDestSubdir("claude-code")
	if !ok {
		t.Fatal(`isolation.CredentialSeedDestSubdir("claude-code") reports no such row`)
	}
	want := filepath.Join(StateHome(workDir), destSubdir)
	if got := InTreeConfigDir(workDir); got != want {
		t.Errorf("InTreeConfigDir(%q) = %q, want %q (the seed destination)", workDir, got, want)
	}
}

// TestInTreeConfigDir_MatchesTheIsolationHomeVarSubdir keeps the in-tree leaf
// name equal to the leaf the WORKTREE axis points CLAUDE_CONFIG_DIR at. The
// two axes root their homes in different places by design; the leaf must not
// also diverge, or claude's own config-home layout would differ per axis.
func TestInTreeConfigDir_MatchesTheIsolationHomeVarSubdir(t *testing.T) {
	hv := isolation.CredentialSeedHomeVars("claude-code")
	if len(hv) != 1 {
		t.Fatalf(`isolation.CredentialSeedHomeVars("claude-code") = %v, want exactly one entry`, hv)
	}
	if hv[0].EnvVar != ConfigDirEnv {
		t.Errorf("claude's isolation HomeVar is %q, want ConfigDirEnv %q", hv[0].EnvVar, ConfigDirEnv)
	}
	workDir := filepath.Join(string(filepath.Separator), "proj")
	if got := filepath.Base(InTreeConfigDir(workDir)); got != hv[0].Subdir {
		t.Errorf("InTreeConfigDir leaf = %q, want the isolation HomeVar Subdir %q", got, hv[0].Subdir)
	}
}

// TestStateHome_NeverTheRealHostHome is the regression this whole phase exists
// to avoid creating: the controlled home is under the PROJECT's .ctxloom/state
// tier, never the user's own ~/.claude. Asserted structurally (a relative
// project path stays relative) so it holds without consulting a real HOME.
func TestStateHome_NeverTheRealHostHome(t *testing.T) {
	got := StateHome("proj")
	if filepath.IsAbs(got) {
		t.Errorf("StateHome(%q) = %q, want a project-relative path — an absolute one means the home escaped the checkout", "proj", got)
	}
	if want := filepath.Join("proj", paths.AppDirName); !strings.HasPrefix(got, want) {
		t.Errorf("StateHome(%q) = %q, want it under %q", "proj", got, want)
	}
	if filepath.Base(got) == ConfigDirName {
		t.Errorf("StateHome(%q) = %q — the state home must not be spelled like claude's own cwd-keyed %q surface", "proj", got, ConfigDirName)
	}
}
