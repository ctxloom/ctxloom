package gitutil

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// The package doc makes claims a reader will act on. They are pinned here,
// because a doc nobody can verify is worth less than no doc: it is believed
// and then quietly stops being true.

// CLAIM: SanitizedEnviron REMOVES the repository-location variables outright,
// rather than blanking them — git treats GIT_DIR="" as still present, so an
// emptied variable is not a removed one. That includes the numbered config
// override pairs git does not list by name.
func TestSanitizedEnviron_RemovesRatherThanBlanks(t *testing.T) {
	stripped := append(slices.Clone(repoLocationEnvVars()), "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0")
	for _, name := range stripped {
		t.Setenv(name, "/somewhere/else")
	}
	t.Setenv("CTXLOOM_GITUTIL_PIN_KEEP", "kept")

	got := map[string]string{}
	for _, kv := range SanitizedEnviron() {
		key, value, _ := strings.Cut(kv, "=")
		got[key] = value
	}

	// Assert by MEMBERSHIP on a parsed map, never by matching the dump: the
	// process environment on a developer machine carries live credentials.
	for _, name := range stripped {
		if _, present := got[name]; present {
			t.Errorf("%s survived sanitization (blanking is not removal: git reads GIT_DIR=\"\" as set)", name)
		}
	}
	assert.Equal(t, "kept", got["CTXLOOM_GITUTIL_PIN_KEEP"],
		"sanitization must strip the location variables and nothing else")
}

// CLAIM: the set is git's own answer, not a list kept here. Asserted as a
// floor rather than an exact match, because it follows the installed git: the
// floor is the variables whose absence from the set is a known hole.
func TestRepoLocationEnvVars_ComeFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	probed, err := probeLocalEnvVars()
	require.NoError(t, err)
	assert.Subset(t, probed, []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
		"GIT_CONFIG_PARAMETERS", configCountEnv, "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SHALLOW_FILE",
	})
	assert.Equal(t, probed, repoLocationEnvVars(), "the sanitiser does not use git's answer")
}

// CLAIM: when git cannot be asked, the sanitiser still strips — every GIT_
// variable, rather than passing them all through or consulting a list kept here.
func TestResolveLocalEnvVars_StripsEveryGitVarWhenGitCannotAnswer(t *testing.T) {
	errNoGit := errors.New("no git")
	for name, probe := range map[string]func() ([]string, error){
		"error":             func() ([]string, error) { return nil, errNoGit },
		"error, an answer":  func() ([]string, error) { return []string{"GIT_DIR"}, errNoGit },
		"empty":             func() ([]string, error) { return nil, nil },
		"empty, not absent": func() ([]string, error) { return []string{}, nil },
	} {
		vars := resolveLocalEnvVars(probe)
		for _, key := range []string{"GIT_DIR", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_KEY_3", "GIT_SOMETHING_NEW"} {
			assert.True(t, isLocalEnvVar(vars, key), "%s: %s passed through", name, key)
		}
		assert.False(t, isLocalEnvVar(vars, "HOME"), "%s: a non-git variable was stripped", name)
	}
}

// CLAIM: an answer from git is taken as it stands, a one-variable answer
// included — never widened to every GIT_ variable.
func TestResolveLocalEnvVars_TakesGitsAnswer(t *testing.T) {
	vars := resolveLocalEnvVars(func() ([]string, error) { return []string{"GIT_DIR"}, nil })
	assert.Equal(t, []string{"GIT_DIR"}, vars)
	assert.False(t, isLocalEnvVar(vars, "GIT_SOMETHING_NEW"))
}

// CLAIM: the numbered config pairs are stripped exactly when git lists their
// counter, and only git's variables are stripped otherwise.
func TestIsLocalEnvVar_PairsFollowTheCounter(t *testing.T) {
	withCount := []string{"GIT_DIR", configCountEnv}
	assert.True(t, isLocalEnvVar(withCount, "GIT_DIR"))
	assert.True(t, isLocalEnvVar(withCount, "GIT_CONFIG_KEY_12"))
	assert.True(t, isLocalEnvVar(withCount, "GIT_CONFIG_VALUE_0"))
	assert.False(t, isLocalEnvVar(withCount, "GIT_SSH_COMMAND"), "a variable git does not list survives")

	withoutCount := []string{"GIT_DIR"}
	assert.False(t, isLocalEnvVar(withoutCount, "GIT_CONFIG_KEY_0"))
}

// CLAIM: this layer reads repositories in process. Its one subprocess is the
// repository-independent probe in localenv.go; if any other subprocess or a
// write operation lands here, the boundary the package doc describes has
// stopped being true and the doc is now actively misleading.
func TestGitutil_SpawnsOnlyTheLocalEnvProbe(t *testing.T) {
	// Resolved from this test's COMPILED-IN source path, not the cwd: a scan
	// rooted at "." finds nothing the moment anything moves the working
	// directory, and a gate that finds nothing passes.
	dir, err := sourcedir.Dir()
	require.NoError(t, err, "could not locate this package's source directory")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	probes := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		body := string(src)
		if filepath.Base(f) == "localenv.go" {
			probes = strings.Count(body, "exec.Command(")
			assert.Contains(t, body, `exec.Command("git", "rev-parse", "--local-env-vars")`)
			continue
		}
		for _, forbidden := range []string{`"os/exec"`, "exec.Command"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s references %s: this layer answers in process; "+
					"anything that must run the git binary belongs in internal/adapters/git", filepath.Base(f), forbidden)
			}
		}
	}
	assert.Equal(t, 1, probes, "localenv.go must spawn exactly the one probe; the scan is not looking at it otherwise")
}
