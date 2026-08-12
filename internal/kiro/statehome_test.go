package kiro

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/paths"
)

const (
	harpA = "ugly-icy-squid"
	harpB = "brave-warm-otter"
)

func testWorkDir() string { return filepath.Join(string(filepath.Separator), "proj") }

// TestSessionHome_IsUnderTheSessionInstanceHome pins kiro's instance to the ONE
// location the per-session model names — <workDir>/.ctxloom/state/<harp>/home/
// kiro — derived from paths.SessionHomePath rather than a repeated literal.
func TestSessionHome_IsUnderTheSessionInstanceHome(t *testing.T) {
	workDir := testWorkDir()
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harpA)
	if err != nil {
		t.Fatalf("paths.SessionHomePath() error = %v", err)
	}
	got, err := SessionHome(workDir, harpA)
	if err != nil {
		t.Fatalf("SessionHome() error = %v", err)
	}
	if want := filepath.Join(root, inTreeHomeLeaf); got != want {
		t.Errorf("SessionHome(%q, %q) = %q, want %q", workDir, harpA, got, want)
	}
}

// TestSessionHome_IsPerSession: two concurrent sessions in one checkout get two
// KIRO_HOMEs. kiro relocates its WHOLE home, so a shared one would let one
// session's agent read and overwrite the other's global agents and steering.
func TestSessionHome_IsPerSession(t *testing.T) {
	workDir := testWorkDir()
	a, err := SessionHome(workDir, harpA)
	if err != nil {
		t.Fatalf("SessionHome(A) error = %v", err)
	}
	b, err := SessionHome(workDir, harpB)
	if err != nil {
		t.Fatalf("SessionHome(B) error = %v", err)
	}
	if a == b {
		t.Errorf("two sessions share one KIRO_HOME (%q); the instance must be keyed by harp", a)
	}
}

// TestSessionHome_RefusesAHarplessCaller is gate (b) at kiro's own resolver.
func TestSessionHome_RefusesAHarplessCaller(t *testing.T) {
	for _, bad := range []string{"", "..", "../.."} {
		got, err := SessionHome(testWorkDir(), bad)
		if err == nil {
			t.Errorf("SessionHome(harp=%q) = %q with no error", bad, got)
		}
		if got != "" {
			t.Errorf("SessionHome(harp=%q) returned %q alongside its error", bad, got)
		}
	}
}

// TestSessionHome_MatchesTheIsolationHomeVarSubdir is kiro's half of the
// writer-agreement pin. kiro has NO copyable credential file (its subscription
// auth lives in a global sqlite keyed off XDGDataHomeEnv — see HomeEnv's doc),
// so there is no copy destination to agree with; what must agree is the leaf
// the WORKTREE axis points KIRO_HOME at, so kiro's home layout is the same
// shape on both axes.
func TestSessionHome_MatchesTheIsolationHomeVarSubdir(t *testing.T) {
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
		t.Errorf("%s is marked GatedOnCreds; the in-tree instance relocates it unconditionally, which would be wrong for a credential-bearing var", HomeEnv)
	}
	workDir := testWorkDir()
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harpA)
	if err != nil {
		t.Fatalf("paths.SessionHomePath() error = %v", err)
	}
	got, err := SessionHome(workDir, harpA)
	if err != nil {
		t.Fatalf("SessionHome() error = %v", err)
	}
	if want := filepath.Join(root, homeVar.Subdir); got != want {
		t.Errorf("SessionHome(%q, %q) = %q, want %q", workDir, harpA, got, want)
	}
}

// TestSessionHome_NeverTheRealHostHome pins the same regression guard claude's
// sibling test does: the instance lives under the PROJECT's .ctxloom/state
// tier, never the user's own ~/.kiro.
func TestSessionHome_NeverTheRealHostHome(t *testing.T) {
	got, err := SessionHome("proj", harpA)
	if err != nil {
		t.Fatalf("SessionHome() error = %v", err)
	}
	if filepath.IsAbs(got) {
		t.Errorf("SessionHome(%q) = %q, want a project-relative path", "proj", got)
	}
	if want := filepath.Join("proj", paths.AppDirName, paths.StateDir); !strings.HasPrefix(got, want) {
		t.Errorf("SessionHome(%q) = %q, want it under %q", "proj", got, want)
	}
}
