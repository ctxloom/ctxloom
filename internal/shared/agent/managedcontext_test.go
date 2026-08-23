package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestStripManagedSection_UnterminatedBeginTrimsBlankPrefix pins the
// unterminated-begin-marker branch: content before an unterminated begin
// marker that is ENTIRELY blank lines must be dropped, not returned as a
// stray "\n". The prior ifNonEmptySuffix checked the UNTRIMMED prefix
// for emptiness while the trimmed value was what actually got returned — for
// "\n\n<begin>...", the trim yields "" but the guard saw "\n\n" (non-empty)
// and appended "\n", so StripManagedSection returned "\n" instead of "".
func TestStripManagedSection_UnterminatedBeginTrimsBlankPrefix(t *testing.T) {
	content := "\n\n" + ManagedContextBegin + "\nunterminated body, no end marker"

	got := StripManagedSection(content)

	assert.Equal(t, "", got, "an all-blank-lines prefix before an unterminated begin marker must not survive as a stray newline")
}

// TestStripManagedSection_UnterminatedBeginPreservesRealPrefix is the sibling
// case: real (non-blank) content before an unterminated begin marker must
// survive, trimmed of trailing blank lines and terminated with exactly one
// newline.
func TestStripManagedSection_UnterminatedBeginPreservesRealPrefix(t *testing.T) {
	content := "real user content\n\n\n" + ManagedContextBegin + "\nunterminated body, no end marker"

	got := StripManagedSection(content)

	assert.Equal(t, "real user content\n", got)
}

// TestWriteManagedContext_PreservesPositionOfTrailingUserContent pins the
// fix: WriteManagedContext documents "content OUTSIDE [the markers] is
// the user's and is preserved byte-for-byte", but content that came AFTER
// the end marker used to be hoisted ABOVE the (re-appended) managed section
// on every rewrite, because the merge always appended the new section at the
// very end regardless of where the old one had been.
func TestWriteManagedContext_PreservesPositionOfTrailingUserContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/proj/CLAUDE.md"
	existing := "A\n" + ManagedContextBegin + "\nold\n" + ManagedContextEnd + "\nB\n"
	require.NoError(t, afero.WriteFile(fs, path, []byte(existing), 0644))

	_, err := WriteManagedContext(fs, path, "CLAUDE.md", "new", "CLAUDE.md")
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	want := "A\n" + ManagedContextBegin + "\nnew\n" + ManagedContextEnd + "\nB\n"
	assert.Equal(t, want, string(data), "user content after the end marker must stay after the managed section, not get hoisted above it")
}

// TestWriteManagedContext_SerializesConcurrentSplices is the D7-remainder
// (N2) proof that WriteManagedContext itself — not just WithFileLock in
// isolation — runs its read-splice-write cycle under the lock. Same
// deterministic-seam idiom as rmw_lock_test.go's
// TestWithFileLock_SerializesRMW_BothWritersEntriesSurvive: writer A takes
// the EXACT lock WithFileLock derives for this target
// (paths.HomePathFor), directly, before writer B's real
// WriteManagedContext call is ever spawned — that physically excludes B
// regardless of goroutine scheduling, so the only role the sleep plays is
// giving a BROKEN implementation time to prove itself broken.
//
// This is the ONE new D7 site that gets its own concurrency test; the other
// three (cli's config-write, cmd/ltk, cmd/taskloom) are exercised through the
// SAME shared WithFileLock primitive rmw_lock_test.go already proves excludes
// concurrent writers — duplicating that proof at every call site would pin
// WithFileLock's behavior four more times without covering anything new.
//
// MUTATION KILL: remove WriteManagedContext's WithFileLock wrapper (call
// writeManagedContextLocked directly), and this test goes red — writer B's
// goroutine completes unexcluded, tripping the "B finished while A still
// held the lock" assertion within the grace window.
func TestWriteManagedContext_SerializesConcurrentSplices(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(path, []byte("user content\n"), 0o644))
	osfs := afero.NewOsFs()

	// Writer A: take the real home lock directly, standing in for
	// WriteManagedContext already being mid-critical-section.
	lockPath, err := paths.HomePathFor(path)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	aLock := flock.New(lockPath)
	require.NoError(t, aLock.Lock())

	bDone := make(chan error, 1)
	go func() {
		_, werr := WriteManagedContext(osfs, path, "CLAUDE.md", "writer-b section", "CLAUDE.md")
		bDone <- werr
	}()

	select {
	case <-bDone:
		t.Fatal("WriteManagedContext completed its read-splice-write while writer A still held the lock — the RMW is not excluded")
	case <-time.After(20 * time.Millisecond):
	}

	require.NoError(t, aLock.Unlock())
	require.NoError(t, <-bDone)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "writer-b section", "writer B's managed section must have landed once it was unblocked")
}
