package gitutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The package doc now makes two claims a reader will act on. Both are pinned
// here, because a doc nobody can verify is worth less than no doc: it is
// believed and then quietly stops being true.

// CLAIM: SanitizedEnviron REMOVES the repository-location variables outright,
// rather than blanking them — git treats GIT_DIR="" as still present, so an
// emptied variable is not a removed one.
func TestSanitizedEnviron_RemovesRatherThanBlanks(t *testing.T) {
	for _, name := range RepoLocationEnvVars {
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
	for _, name := range RepoLocationEnvVars {
		if _, present := got[name]; present {
			t.Errorf("%s survived sanitization (blanking is not removal: git reads GIT_DIR=\"\" as set)", name)
		}
	}
	assert.Equal(t, "kept", got["CTXLOOM_GITUTIL_PIN_KEEP"],
		"sanitization must strip the location variables and nothing else")
}

// CLAIM: the list is the single home of these variables, and it is the list
// that matters — every variable git honours to redirect WHICH repository it
// operates on. Losing an entry silently re-opens the hole, so the membership
// is pinned rather than just the count.
func TestRepoLocationEnvVars_Membership(t *testing.T) {
	want := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY"}
	assert.ElementsMatch(t, want, RepoLocationEnvVars)
}

// CLAIM: this is the layer that spawns nothing. If a write operation or a
// subprocess ever lands here, the boundary the package doc describes has
// stopped being true and the doc is now actively misleading.
func TestGitutil_SpawnsNoSubprocesses(t *testing.T) {
	// Resolved from this test's COMPILED-IN source path, not the cwd: a scan
	// rooted at "." finds nothing the moment anything moves the working
	// directory, and a gate that finds nothing passes.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "could not locate this test's own source file")
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "gitutil.go"))
	require.NoError(t, err)
	require.NotEmpty(t, src, "the scan found an empty file; it is not scanning what it thinks")
	body := string(src)
	for _, forbidden := range []string{`"os/exec"`, "exec.Command"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("gitutil.go references %s: this layer answers in process; "+
				"anything that must run the git binary belongs in internal/git", forbidden)
		}
	}
}
