//go:build !windows

package plans

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// showFixture points HOME at a temp tree and returns the sessions root and one
// harp directory inside it. Nothing here may touch the real ~/.ctxloom.
func showFixture(t *testing.T) (root, harpDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Fixture check: HomeSessionsDir must actually follow HOME here, or every
	// assertion below would be about the real home directory instead.
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.True(t, filepath.HasPrefix(root, home),
		"the sessions root did not follow HOME (%s); refusing to test against the real home", root)

	harpDir = filepath.Join(root, "vital-deaf-stunt")
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	return root, harpDir
}

// TestShow_RefusesSymlinkOutOfTheSessionsDir pins the guarantee Show's doc
// makes. A lexical containment check passes for a path that sits under the
// root as a STRING while pointing anywhere on the filesystem, so the check has
// to be made on the resolved path.
func TestShow_RefusesSymlinkOutOfTheSessionsDir(t *testing.T) {
	_, harpDir := showFixture(t)

	outside := filepath.Join(t.TempDir(), "secret")
	const secret = "credentials the plan reader must never hand back"
	require.NoError(t, os.WriteFile(outside, []byte(secret), 0o600))

	link := filepath.Join(harpDir, "innocent"+paths.PlanFileExt)
	require.NoError(t, os.Symlink(outside, link))

	// Fixture check: the escape route is real — the symlink resolves outside
	// the root and is readable by ordinary means. Without this the refusal
	// below could be a missing file rather than a rejected traversal.
	resolved, err := filepath.EvalSymlinks(link)
	require.NoError(t, err)
	require.NotEqual(t, link, resolved)
	viaLink, err := os.ReadFile(link)
	require.NoError(t, err)
	require.Equal(t, secret, string(viaLink), "the fixture does not actually reach outside the root")

	got, err := Show(link)
	require.Error(t, err, "a symlink out of the sessions directory must be refused")
	assert.ErrorContains(t, err, "outside the sessions directory")
	assert.Empty(t, got, "no bytes of the target may be returned")
}

// TestShow_AllowsSymlinkWithinTheSessionsDir keeps the fix from being a blanket
// symlink ban: a plan linked from elsewhere inside the sessions tree is still
// a plan the user may read.
func TestShow_AllowsSymlinkWithinTheSessionsDir(t *testing.T) {
	_, harpDir := showFixture(t)

	target := filepath.Join(harpDir, "real"+paths.PlanFileExt)
	require.NoError(t, os.WriteFile(target, []byte("---\ntitle: Real\n---\nbody\n"), 0o644))

	link := filepath.Join(harpDir, "alias"+paths.PlanFileExt)
	require.NoError(t, os.Symlink(target, link))

	got, err := Show(link)
	require.NoError(t, err)
	assert.Contains(t, got, "title: Real")
}

// TestShow_RefusesNonRegularFile pins the other half of the doc's promise.
// os.ReadFile on a FIFO blocks until a writer appears, so an unchecked
// non-regular file turns `plan show` from a refusal into an indefinite hang.
// The call is bounded here so that a regression fails the test instead of
// parking it for the whole test timeout.
func TestShow_RefusesNonRegularFile(t *testing.T) {
	_, harpDir := showFixture(t)

	fifo := filepath.Join(harpDir, "pipe"+paths.PlanFileExt)
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))

	// Fixture check: it really is a FIFO from the filesystem's point of view.
	info, err := os.Lstat(fifo)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe, "the fixture is not a FIFO")

	type result struct {
		content string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		content, serr := Show(fifo)
		done <- result{content, serr}
	}()

	select {
	case got := <-done:
		require.Error(t, got.err, "a non-regular file must be refused")
		assert.ErrorContains(t, got.err, "not a regular file")
		assert.Empty(t, got.content)
	case <-time.After(10 * time.Second):
		t.Fatal("Show blocked on a FIFO instead of refusing it")
	}
}
