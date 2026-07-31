package projectroot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestWorkDirWithBoundary_BadOverrideIsNotABoundary pins the meaning of the
// found bit against the most plausible wrong "fix" for it.
//
// resolve() distinguishes three CTXLOOM_ROOT states (unset / set-but-unusable /
// set-and-valid) and FromEnv collapses the first two to ("", false) after
// warning about the second. The found bit inherits that collapse, and the
// tempting reading is that it should not: that "the user named a root" ought to
// count as a boundary even when the root does not exist.
//
// It must not. found=true is what licenses MINTING a project identity — a task
// log, plans, sessions keyed on that directory — and the directory actually
// resolved in this case is the bare cwd, "whatever directory the shell happened
// to be in". Reporting a boundary for it would key a permanent identity on an
// arbitrary path on the strength of a value that named nothing. The user learns
// about the bad override from the warning (per offending value), not from a
// boundary bit that lies.
func TestWorkDirWithBoundary_BadOverrideIsNotABoundary(t *testing.T) {
	testsupport.ProjectDir(t) // isolate env + chdir to a fresh non-git temp dir

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	bad := filepath.Join(t.TempDir(), "boundary-no-such-root")
	_, statErr := os.Stat(bad)
	require.True(t, os.IsNotExist(statErr), "fixture is not hostile: %s exists", bad)
	t.Setenv(EnvVar, bad)

	root, found, err := WorkDirWithBoundary()
	require.NoError(t, err, "a bad override never blocks resolution")
	assert.False(t, found,
		"an override naming a path that does not exist is not a project boundary; "+
			"found=true here would mint a permanent identity keyed on the bare cwd")
	assert.NotEqual(t, bad, root, "the unusable override must not become the root")
	assert.Contains(t, sink.String(), bad,
		"the user learns about the bad override from the warning, not from the boundary bit")
}

// TestWorkDirWithBoundary_BadOverrideFallsThroughToGitRoot is the same
// collapse seen from the case where a real boundary does exist below the
// override: the override is discarded, the git root wins, and found is true
// because of the REPOSITORY, not because of the variable.
func TestWorkDirWithBoundary_BadOverrideFallsThroughToGitRoot(t *testing.T) {
	repo := testsupport.ProjectDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "refs"), 0o755))

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	bad := filepath.Join(t.TempDir(), "boundary-no-such-root-2")
	t.Setenv(EnvVar, bad)

	root, found, err := WorkDirWithBoundary()
	require.NoError(t, err)
	assert.True(t, found, "the repository is a real boundary even though the override was not")
	assert.NotEqual(t, bad, root)
	assert.Contains(t, sink.String(), bad, "the discarded override is still reported")
}
