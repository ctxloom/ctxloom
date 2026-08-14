package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// fakePlacement is a local agent.Placement test-double: Dir() returns a fixed
// directory (a test temp dir). It stands in for the package-agent placements
// (ephemeralPlacement / cwdPlacement) which are unexported and not constructible
// from here.
type fakePlacement struct{ dir string }

func (p fakePlacement) Dir() string { return p.dir }

// TestAppendFlagDelivery_DeliverContext verifies the append-flag strategy frames
// the context via agent.FrameProjectContext, writes it into the injected
// Placement as <hash>.sysprompt.md, exposes that path via Path(), and removes
// the file on Cleanup.
func TestAppendFlagDelivery_DeliverContext(t *testing.T) {
	dir := t.TempDir()
	d := newAppendFlagDelivery(fakePlacement{dir: dir}, nil)

	const ctx = "# Rules\nthe secret color is vermilion"
	handle, err := d.DeliverContext(ctx)
	require.NoError(t, err)

	// Path() is <dir>/<something>.sysprompt.md and points at a real file.
	path := d.Path()
	require.NotEmpty(t, path)
	assert.Equal(t, dir, filepath.Dir(path))
	assert.True(t, strings.HasSuffix(path, agent.SCMFramedContextSuffix),
		"filename must carry the framed-context suffix")
	require.FileExists(t, path)

	// The written bytes equal today's framing of the same context.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, agent.FrameProjectContext(ctx), string(data))

	// Cleanup removes exactly the written file.
	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, path)
}

// TestAppendFlagDelivery_Deterministic verifies the filename is a deterministic
// function of the framed content: the same context yields the same path.
func TestAppendFlagDelivery_Deterministic(t *testing.T) {
	dir := t.TempDir()
	const ctx = "stable content"

	d1 := newAppendFlagDelivery(fakePlacement{dir: dir}, nil)
	_, err := d1.DeliverContext(ctx)
	require.NoError(t, err)

	d2 := newAppendFlagDelivery(fakePlacement{dir: dir}, nil)
	_, err = d2.DeliverContext(ctx)
	require.NoError(t, err)

	assert.Equal(t, d1.Path(), d2.Path())
}

// TestAppendFlagDelivery_DeliverContext_PreservesExistingMode pins the switch
// from a raw afero.WriteFile(..., 0o644) to agent.AtomicWriteFile: the latter
// preserves an EXISTING destination file's mode across the rewrite, while a
// raw afero.WriteFile with a hardcoded 0o644 would silently reset it every
// time. The deterministic hash filename means a second delivery of the SAME
// framed content lands at the SAME path, so pre-seeding that exact path with
// a non-default mode and re-delivering the identical context is how this
// distinguishes the two write paths without reaching into AtomicWriteFile's
// own internals.
//
// MUTATION KILL: revert DeliverContext's write to afero.WriteFile(d.fs, path,
// []byte(framed), 0o644) and this goes red — the mode comes back 0o644
// instead of the pre-seeded 0o640.
func TestAppendFlagDelivery_DeliverContext_PreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	const ctx = "# Rules\nthe secret color is vermilion"
	framed := agent.FrameProjectContext(ctx)
	sum := sha256.Sum256([]byte(framed))
	name := hex.EncodeToString(sum[:8]) + agent.SCMFramedContextSuffix
	path := filepath.Join(dir, name)

	// Pre-seed the exact deterministic destination with a mode a raw
	// afero.WriteFile(..., 0o644) call could never produce as ITS result.
	require.NoError(t, os.WriteFile(path, []byte("stale"), 0o640))

	d := newAppendFlagDelivery(fakePlacement{dir: dir}, nil)
	_, err := d.DeliverContext(ctx)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(),
		"AtomicWriteFile preserves an existing file's mode across the rewrite; a raw afero.WriteFile(...,0o644) would reset it")
}

// TestAppendFlagDelivery_EmptyContextIsNoop verifies empty context frames to ""
// (nothing to deliver): no file is written, Path() stays empty, and Cleanup is a
// no-op.
func TestAppendFlagDelivery_EmptyContextIsNoop(t *testing.T) {
	dir := t.TempDir()
	d := newAppendFlagDelivery(fakePlacement{dir: dir}, nil)

	handle, err := d.DeliverContext("")
	require.NoError(t, err)
	assert.Empty(t, d.Path())
	require.NoError(t, handle.Cleanup())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "empty context must not write any file")
}
