package claude

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

// TestSessionConfigDir_IsUnderTheSessionInstanceHome pins claude's instance to
// the ONE location the per-session model names — <workDir>/.ctxloom/state/
// <harp>/home/claude — derived from paths.SessionHomePath rather than from a
// literal spelled twice.
func TestSessionConfigDir_IsUnderTheSessionInstanceHome(t *testing.T) {
	workDir := testWorkDir()
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harpA)
	if err != nil {
		t.Fatalf("paths.SessionHomePath() error = %v", err)
	}
	got, err := SessionConfigDir(workDir, harpA)
	if err != nil {
		t.Fatalf("SessionConfigDir() error = %v", err)
	}
	if want := filepath.Join(root, inTreeConfigLeaf); got != want {
		t.Errorf("SessionConfigDir(%q, %q) = %q, want %q", workDir, harpA, got, want)
	}
}

// TestSessionConfigDir_IsPerSession is the property the per-project home did
// not have: two concurrent sessions in ONE checkout get two homes, so neither
// reads the other's copied credentials or clobbers its generated config.
func TestSessionConfigDir_IsPerSession(t *testing.T) {
	workDir := testWorkDir()
	a, err := SessionConfigDir(workDir, harpA)
	if err != nil {
		t.Fatalf("SessionConfigDir(A) error = %v", err)
	}
	b, err := SessionConfigDir(workDir, harpB)
	if err != nil {
		t.Fatalf("SessionConfigDir(B) error = %v", err)
	}
	if a == b {
		t.Errorf("two sessions share one CLAUDE_CONFIG_DIR (%q); the instance must be keyed by harp, not by project", a)
	}
	if !strings.Contains(a, harpA) {
		t.Errorf("SessionConfigDir(%q) = %q does not contain the harp", harpA, a)
	}
}

// TestSessionConfigDir_RefusesAHarplessCaller is gate (b) at this engine's own
// resolver: there is no session-less instance, and no shared fallback, because
// a shared fallback is exactly the durable per-project home the model retired.
func TestSessionConfigDir_RefusesAHarplessCaller(t *testing.T) {
	for _, bad := range []string{"", "..", "../.."} {
		got, err := SessionConfigDir(testWorkDir(), bad)
		if err == nil {
			t.Errorf("SessionConfigDir(harp=%q) = %q with no error", bad, got)
		}
		if got != "" {
			t.Errorf("SessionConfigDir(harp=%q) returned %q alongside its error", bad, got)
		}
	}
}

// TestSessionConfigDir_IsTheSeedDestination is the writer-agreement pin: the
// value ctxloom hands claude as CLAUDE_CONFIG_DIR and the directory isolation's
// one-way copy-in writes .credentials.json into MUST be the same directory, or
// the engine reads a home nothing prepared. Both sides are derived here from
// the ONE helper plus the ONE registry.
func TestSessionConfigDir_IsTheSeedDestination(t *testing.T) {
	workDir := testWorkDir()
	destSubdir, ok := isolation.CredentialSeedDestSubdir("claude-code")
	if !ok {
		t.Fatal(`isolation.CredentialSeedDestSubdir("claude-code") reports no such row`)
	}
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harpA)
	if err != nil {
		t.Fatalf("paths.SessionHomePath() error = %v", err)
	}
	got, err := SessionConfigDir(workDir, harpA)
	if err != nil {
		t.Fatalf("SessionConfigDir() error = %v", err)
	}
	if want := filepath.Join(root, destSubdir); got != want {
		t.Errorf("SessionConfigDir(%q, %q) = %q, want the copy-in destination %q", workDir, harpA, got, want)
	}
}

// TestSessionConfigDir_MatchesTheIsolationHomeVarSubdir keeps the in-tree leaf
// equal to the leaf the WORKTREE axis points CLAUDE_CONFIG_DIR at. The two axes
// root their homes in different places by design; the leaf must not also
// diverge, or claude's config-home layout would differ per axis.
func TestSessionConfigDir_MatchesTheIsolationHomeVarSubdir(t *testing.T) {
	hv := isolation.CredentialSeedHomeVars("claude-code")
	if len(hv) != 1 {
		t.Fatalf(`isolation.CredentialSeedHomeVars("claude-code") = %v, want exactly one entry`, hv)
	}
	if hv[0].EnvVar != ConfigDirEnv {
		t.Errorf("claude's isolation HomeVar is %q, want ConfigDirEnv %q", hv[0].EnvVar, ConfigDirEnv)
	}
	got, err := SessionConfigDir(testWorkDir(), harpA)
	if err != nil {
		t.Fatalf("SessionConfigDir() error = %v", err)
	}
	if base := filepath.Base(got); base != hv[0].Subdir {
		t.Errorf("SessionConfigDir leaf = %q, want the isolation HomeVar Subdir %q", base, hv[0].Subdir)
	}
}

// TestSessionConfigDir_NeverTheRealHostHome is the regression the whole model
// exists to avoid: the instance is under the PROJECT's .ctxloom/state tier,
// never the user's own ~/.claude. Asserted structurally (a relative project
// path stays relative) so it holds without consulting a real HOME.
func TestSessionConfigDir_NeverTheRealHostHome(t *testing.T) {
	got, err := SessionConfigDir("proj", harpA)
	if err != nil {
		t.Fatalf("SessionConfigDir() error = %v", err)
	}
	if filepath.IsAbs(got) {
		t.Errorf("SessionConfigDir(%q) = %q, want a project-relative path — an absolute one means the instance escaped the checkout", "proj", got)
	}
	if want := filepath.Join("proj", paths.AppDirName, paths.StateDir); !strings.HasPrefix(got, want) {
		t.Errorf("SessionConfigDir(%q) = %q, want it under %q", "proj", got, want)
	}
	if filepath.Base(got) == ConfigDirName {
		t.Errorf("SessionConfigDir(%q) = %q — the instance must not be spelled like claude's own cwd-keyed %q surface", "proj", got, ConfigDirName)
	}
}
