package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLinkTranscriptIntoHarpDir_LinkTracksTheCanonicalLeafName pins WHY the
// convenience symlink is called what it is.
//
// The link exists to present the canonical transcript's name at the harp
// directory root, one level above the file itself: a reader who knows
// "transcript.jsonl" finds it whether or not the physical transcript lives
// under persist/. Its name is therefore not an independent choice — it is the
// canonical leaf name, and it must follow that name wherever it goes. A link
// still called transcript.jsonl after the canonical file became something else
// is a name that no longer means what it promises, and nothing would say so:
// creating a symlink under a stale name succeeds exactly as loudly as creating
// one under a current name.
//
// Note this assertion CANNOT be driven red by the collapse alone, and no red is
// claimed for it: the literal it replaces was byte-identical to the constant,
// so before and after are the same string. What the collapse changes is whether
// the two can DRIFT — verified by rewriting the constant and observing that the
// link name follows it only once the literal is gone.
func TestLinkTranscriptIntoHarpDir_LinkTracksTheCanonicalLeafName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)
	const harp = "swift-amber-falcon"

	// The target must live OUTSIDE the harp dir, or the linker deliberately
	// declines to add a second name for an already-addressable file — and the
	// test would assert nothing while looking like it passed.
	target := filepath.Join(t.TempDir(), "engine-native.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("{}\n"), 0o644))

	linkTranscriptIntoHarpDir(harp, target)

	harpDir := filepath.Join(home, paths.AppDirName, paths.SessionsDir, harp)
	link := filepath.Join(harpDir, paths.CanonicalTranscriptFileName)

	got, err := os.Readlink(link)
	require.NoError(t, err,
		"no symlink at %s: the convenience link is not named for the canonical transcript", link)
	assert.Equal(t, target, got, "the link must point at the engine's live transcript")

	// The link sits one directory ABOVE the canonical file, sharing its leaf
	// name deliberately rather than by coincidence.
	canonical, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	assert.Equal(t, filepath.Base(canonical), filepath.Base(link),
		"the link carries the canonical transcript's leaf name")
	assert.Equal(t, harpDir, filepath.Dir(link))
	assert.NotEqual(t, filepath.Dir(canonical), filepath.Dir(link),
		"the link is a second name one level up, not the canonical file itself")
}
