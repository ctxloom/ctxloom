package kiro

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestStateHome_IsTheProjectScopedEngineHome pins kiro's project-scoped engine
// home to the ONE location the engine-home policy names
// (paths.EngineStateHome), keyed by kiro's REGISTERED backend name.
func TestStateHome_IsTheProjectScopedEngineHome(t *testing.T) {
	workDir := filepath.Join(string(filepath.Separator), "proj")
	want := paths.EngineStateHome(filepath.Join(workDir, paths.AppDirName), "kiro")
	if got := StateHome(workDir); got != want {
		t.Errorf("StateHome(%q) = %q, want %q", workDir, got, want)
	}
}

// TestInTreeHome_MatchesTheIsolationHomeVarSubdir is kiro's half of the
// writer-agreement pin. kiro has NO seedable credential file (its subscription
// auth lives in a global sqlite keyed off XDGDataHomeEnv — see HomeEnv's doc),
// so there is no seed destination to agree with; what must agree is the leaf
// name the WORKTREE axis points KIRO_HOME at, so kiro's home layout is the same
// shape on both axes and a future static writer resolving through InTreeHome
// lands where the run path points the engine.
func TestInTreeHome_MatchesTheIsolationHomeVarSubdir(t *testing.T) {
	var homeVar isolation.CredentialSeedHomeVar
	for _, hv := range isolation.CredentialSeedHomeVars("kiro") {
		if hv.EnvVar == HomeEnv {
			homeVar = hv
		}
	}
	if homeVar.EnvVar == "" {
		t.Fatalf(`isolation.CredentialSeedHomeVars("kiro") carries no %s entry`, HomeEnv)
	}
	if homeVar.GatedOnCreds {
		t.Errorf("%s is marked GatedOnCreds; the in-tree home relocates it unconditionally, which would be wrong for a credential-bearing var", HomeEnv)
	}
	workDir := filepath.Join(string(filepath.Separator), "proj")
	want := filepath.Join(StateHome(workDir), homeVar.Subdir)
	if got := InTreeHome(workDir); got != want {
		t.Errorf("InTreeHome(%q) = %q, want %q", workDir, got, want)
	}
}

// TestInTreeHome_NeverTheRealHostHome pins the same regression guard claude's
// sibling test does: the controlled home lives under the PROJECT's
// .ctxloom/state tier, never the user's own ~/.kiro.
func TestInTreeHome_NeverTheRealHostHome(t *testing.T) {
	got := InTreeHome("proj")
	if filepath.IsAbs(got) {
		t.Errorf("InTreeHome(%q) = %q, want a project-relative path", "proj", got)
	}
	if want := filepath.Join("proj", paths.AppDirName); !strings.HasPrefix(got, want) {
		t.Errorf("InTreeHome(%q) = %q, want it under %q", "proj", got, want)
	}
}
