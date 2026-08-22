package operations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// This file pins a fix: a live transcript.Recorder holds a SHARED filelock on the
// canonical transcript's default path for its whole lifetime
// (fileRecorder.ensureFile), and RefreshVendorTranscript's rebuild takes the
// matching EXCLUSIVE filelock.TryLock before replacing that same file
// (convertVendorTranscript's ownership probe). The two can therefore never
// both proceed at once: a rebuild that would have renamed a fresh conversion
// over the recorder's open inode — silently orphaning it, losing every event
// recorded afterward, exit 0, no error — now backs off instead.

// TestRefreshVendorTranscript_SkipsRebuildWhileALiveRecorderOwnsTheCanonicalTranscript
// exercises that fix end to end: a live default-path Recorder is
// open on harp's canonical transcript when a refresh races in. The rebuild
// must skip (not error), the recorder's inode must survive untouched, and
// events recorded AFTER the skipped rebuild must still land in that exact
// file — the loss this defect caused is now structurally impossible rather
// than merely rare.
//
// Mutation kill: deleting the `if refresh { ... TryLock ... }` block in
// convertVendorTranscript (always rebuild, as before the fix) turns this red
// — the rebuild would replace canonPath's inode (SameFile fails) and the
// post-skip write would land in a file nothing reads (Contains fails).
// Verified by hand: commenting out the probe block reproduces both failures.
func TestRefreshVendorTranscript_SkipsRebuildWhileALiveRecorderOwnsTheCanonicalTranscript(t *testing.T) {
	testsupport.Isolate(t)
	harp := "easeful-dial-harp"
	e := claudeEntry(harp, claudeFixturePath)

	// A live structured/ACP recorder opens harp's DEFAULT canonical path —
	// exactly what internal/lm/grpc/chat.go's GRPCClient.Chat and
	// internal/agentcoord/coord/enginehost.go's adapt do for a live
	// session — and holds it open (Record, no Close yet) the way a
	// still-running chat would.
	rec, err := transcript.NewRecorder(harp, e.Backend)
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeAssistant,
		Content: "live turn recorded before the rebuild races in",
	}}))

	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	before, err := os.Stat(canonPath)
	require.NoError(t, err)

	// The recover path's refresh races in while the recorder is still open
	// — the exact collision this test guards against (operations.
	// RefreshVendorTranscript vs. the live O_APPEND fd).
	converted, err := RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err, "a skipped rebuild is not an error — the caller must proceed against the existing canonical")
	assert.False(t, converted, "the rebuild must SKIP while a live recorder owns the canonical transcript")

	after, err := os.Stat(canonPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the rebuild must not rename a fresh file over the recorder's open inode")

	// The recorder keeps writing after the skipped rebuild. Those events
	// must land in the SAME file a reader will resolve — the payload
	// assertion the plan calls for, zero-length guarded by canonicalLines'
	// require.NotEmpty below.
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeAssistant,
		Content: "live turn recorded after the rebuild was skipped",
	}}))
	require.NoError(t, rec.Close())

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines)
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "live turn recorded before the rebuild races in")
	assert.Contains(t, joined, "live turn recorded after the rebuild was skipped",
		"an event recorded after the skipped rebuild must still be captured — this is the exact loss easeful-dial reported")
}

// TestRefreshVendorTranscript_ProceedsOnceTheLiveRecorderCloses asserts the
// probe is a live point-in-time check, not a permanent refusal: once
// Recorder.Close releases the shared lock, the next refresh proceeds and
// actually rebuilds.
//
// Mutation kill: removing the unlock call from fileRecorder.Close (or
// forgetting to clear it, so a second Close double-releases instead of the
// first one actually releasing) leaves the shared lock held forever, and the
// second RefreshVendorTranscript call here would keep skipping — converted
// would stay false — turning this red.
func TestRefreshVendorTranscript_ProceedsOnceTheLiveRecorderCloses(t *testing.T) {
	testsupport.Isolate(t)
	harp := "easeful-dial-close-harp"
	e := claudeEntry(harp, claudeFixturePath)

	rec, err := transcript.NewRecorder(harp, e.Backend)
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeAssistant, Content: "live turn",
	}}))

	converted, err := RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.False(t, converted, "must still be skipped while the recorder is open")

	require.NoError(t, rec.Close(), "Close must release the ownership lock")

	converted, err = RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted, "once the recorder releases its lock, the rebuild must proceed")
}

// TestRefreshVendorTranscript_ConcurrentRebuildsSerialize pins the accepted
// side effect the design calls out: TryLock's exclusivity also serializes
// two concurrent RefreshVendorTranscript calls against each other, not just
// a recorder against a rebuild. Driven deterministically via the lock seam
// itself (a stand-in holder) rather than racing goroutines against
// wall-clock timing, per this project's standing note that a lock-seam
// assertion beats a flaky race for determinism.
func TestRefreshVendorTranscript_ConcurrentRebuildsSerialize(t *testing.T) {
	testsupport.Isolate(t)
	harp := "concurrent-rebuild-harp"
	e := claudeEntry(harp, claudeFixturePath)

	// Seed a canonical transcript so canonicalDestination(refresh=true) has
	// a current-named file to resolve, matching the shape a real second
	// rebuild would see.
	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)

	dest, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)

	// Stand in for "a first rebuild is already in progress": hold the exact
	// exclusive lock convertVendorTranscript's probe takes.
	holder, acquired, err := filelock.TryLock(paths.PathFor(dest))
	require.NoError(t, err)
	require.True(t, acquired)
	defer holder()

	converted, err = RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err, "losing the race to a concurrent rebuild is not an error")
	assert.False(t, converted, "a second rebuild must skip while another one already holds the exclusive lock")
}
