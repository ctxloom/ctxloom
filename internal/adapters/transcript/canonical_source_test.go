package transcript

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeCanonicalFixture records one real assistant turn (via the actual
// Recorder writer, not hand-rolled JSONL) to harp's canonical
// transcript path, so these tests exercise the real writer/reader contract.
func writeCanonicalFixture(t *testing.T, harp, engine, content string) {
	t.Helper()
	rec, err := NewRecorder(safefs.New(), harp, engine)
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{
		Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: content},
	}))
	require.NoError(t, rec.Close())
}

// writeCorruptCanonicalFixture leaves harp with a canonical transcript file
// that EXISTS but cannot be read: a record carrying an unknown schema version,
// which the reader refuses outright rather than guessing at.
func writeCorruptCanonicalFixture(t *testing.T, harp string) {
	t.Helper()
	writeCanonicalFixture(t, harp, "claude-code", "about to be clobbered")
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(`{"v":9999,"ts":"2026-01-01T00:00:00Z"}`+"\n"), 0o600))
}

// TestCanonicalFallbackSource_GetSession_CorruptCanonicalSurfaces pins that
// the FIRST canonical read's error is not discarded: doing so turns "your
// transcript is corrupt and I refuse to guess at it" into "there is no
// canonical transcript for this session" — the one message that tells the
// user to stop looking.
func TestCanonicalFallbackSource_GetSession_CorruptCanonicalSurfaces(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-corrupt", "/proj", "backend-uuid-9")
	writeCorruptCanonicalFixture(t, "harp-corrupt")

	src := NewCanonicalFallbackSource("/proj", store)

	_, err := src.GetSession(ctx, "harp-corrupt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version", "the real cause must reach the caller")
	assert.NotContains(t, err.Error(), "no canonical transcript",
		"an unreadable transcript is not an absent one")
}

// TestCanonicalFallbackSource_GetSession_AbsentCanonicalStaysAbsent is
// the other half: a genuinely uncaptured session must still report absence, not
// a transcript-read failure. Surfacing the first error unconditionally would
// have turned every miss into a scary parse error.
func TestCanonicalFallbackSource_GetSession_AbsentCanonicalStaysAbsent(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-uncaptured", "/proj", "backend-uuid-10")

	src := NewCanonicalFallbackSource("/proj", store)

	_, err := src.GetSession(ctx, "harp-uncaptured")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no canonical transcript")
}

// mintBoundHarp registers harp under projectDir in store with the given
// bound backend session id — AssignHarp mints its own name, so Rename
// retargets it to the exact name the test wants (mirrors internal/adapters/transcript's
// own history_test.go `mint` helper).
func mintBoundHarp(t *testing.T, store *sessions.MemStore, harp, projectDir, sessionID string) {
	t.Helper()
	e, err := store.AssignHarp(projectDir, "test-engine")
	require.NoError(t, err)
	require.NoError(t, store.Rename(e.HarpName, harp))
	require.NoError(t, store.BindSession(harp, sessionID, ""))
}

// TestCanonicalFallbackSource_GetSession_PrefersCanonical is the core
// S4 selection-rule proof: a backend-native session id that
// resolves to a harp WITH a captured canonical transcript is served from
// canonical.
func TestCanonicalFallbackSource_GetSession_PrefersCanonical(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-canonical", "/proj", "backend-uuid-1")
	writeCanonicalFixture(t, "harp-canonical", "claude-code", "REAL-CANONICAL-PAYLOAD")

	src := NewCanonicalFallbackSource("/proj", store)

	sess, err := src.GetSession(ctx, "backend-uuid-1")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "REAL-CANONICAL-PAYLOAD", sess.Entries[0].Content, "payload must survive the round trip, not just a non-empty session")
}

// TestCanonicalFallbackSource_GetSession_ResolvesHarpDirectly proves that
// `memory list`'s SESSION ID column literally displays the
// HARP for a canonical-backed session (CanonicalHistory.ListSessions sets
// meta.ID = harp, never the backend-native session id), so `memory show
// <that harp>` must resolve — not just the never-surfaced backend-native id
// the sessionID-only reverse lookup can match. The harp resolves via the
// canonical leg directly, without the index's reverse SessionID->harp lookup.
func TestCanonicalFallbackSource_GetSession_ResolvesHarpDirectly(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-direct", "/proj", "backend-uuid-direct")
	writeCanonicalFixture(t, "harp-direct", "claude-code", "DIRECT-HARP-PAYLOAD")

	src := NewCanonicalFallbackSource("/proj", store)

	// The caller passes the HARP, exactly what `memory list` shows — not the
	// backend-native session id ("backend-uuid-direct") the pre-fix code
	// required.
	sess, err := src.GetSession(ctx, "harp-direct")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "DIRECT-HARP-PAYLOAD", sess.Entries[0].Content)
}

// TestCanonicalFallbackSource_GetSession_HarpFirstDoesNotBreakSessionIDPath
// proves the harp-first attempt is purely additive: a genuine backend-native
// session id (which will never resolve as a harp) must still fall through to
// the existing reverse-index resolution, unchanged.
func TestCanonicalFallbackSource_GetSession_HarpFirstDoesNotBreakSessionIDPath(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-canonical-2", "/proj", "backend-uuid-still-works")
	writeCanonicalFixture(t, "harp-canonical-2", "claude-code", "STILL-WORKS-PAYLOAD")

	src := NewCanonicalFallbackSource("/proj", store)

	sess, err := src.GetSession(ctx, "backend-uuid-still-works")
	require.NoError(t, err)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "STILL-WORKS-PAYLOAD", sess.Entries[0].Content)
}

// TestCanonicalFallbackSource_GetSession_RotatedAwayIDResolvesToHarp pins
// that a backend-native id the harp was PREVIOUSLY bound to — displaced into
// Entry.Rotations by a /clear rebind — still reaches that harp's canonical
//
//	This is the id recover_session targets after a context wipe
//
// (the pre-clear thread is exactly what the caller lost), so if the reverse
// lookup matched only the CURRENT binding, recovery would report "no
// context" at the one moment it was needed. The caller-supplied id must come
// back unchanged, matching the current-binding path.
func TestCanonicalFallbackSource_GetSession_RotatedAwayIDResolvesToHarp(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-rotated", "/proj", "sess-preclear")
	// Only a binder naming a transcript file may re-point (BindSession's
	// rebind rule), so an id-only rebind would leave the pre-clear id CURRENT
	// and this test would pass without ever exercising a rotation.
	require.NoError(t, store.BindSession("harp-rotated", "sess-postclear", "/proj/postclear.jsonl"))
	entry, err := store.Find("harp-rotated")
	require.NoError(t, err)
	require.Equal(t, "sess-postclear", entry.SessionID, "fixture must have rotated the pre-clear id away")
	writeCanonicalFixture(t, "harp-rotated", "claude-code", "PRECLEAR-THREAD-MARKER")

	src := NewCanonicalFallbackSource("/proj", store)

	sess, err := src.GetSession(ctx, "sess-preclear")
	require.NoError(t, err)
	assert.Equal(t, "sess-preclear", sess.ID)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "PRECLEAR-THREAD-MARKER", sess.Entries[0].Content)
}

// TestCanonicalFallbackSource_CurrentSession_PrefersCanonical mirrors the
// GetSession proof for the no-explicit-id "current session" path.
func TestCanonicalFallbackSource_CurrentSession_PrefersCanonical(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-current", "/proj", "backend-uuid-3")
	writeCanonicalFixture(t, "harp-current", "claude-code", "CURRENT-CANONICAL-PAYLOAD")

	src := NewCanonicalFallbackSource("/proj", store)

	sess, err := src.CurrentSession(ctx)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "harp-current", sess.ID)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "CURRENT-CANONICAL-PAYLOAD", sess.Entries[0].Content)
}

// erroringListStore embeds a real MemStore (satisfying every other
// sessions.Store method faithfully via promotion) but forces ListForProject
// to fail — the only hermetic way to make CanonicalHistory.ListSessions
// return a genuine error, since MemStore's own ListForProject never fails.
type erroringListStore struct {
	*sessions.MemStore
	err error
}

func (e *erroringListStore) ListForProject(projectDir string) ([]sessions.Entry, error) {
	return nil, e.err
}

// TestCanonicalFallbackSource_ListSessions_CanonicalErrorPropagates pins that
// a failed canonical listing is reported, not returned as (nil, nil) — a
// confident "no sessions" indistinguishable from a project that genuinely has
// none. Canonical is the only source, so its failure IS the failure.
func TestCanonicalFallbackSource_ListSessions_CanonicalErrorPropagates(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := &erroringListStore{MemStore: sessions.NewMemStore(), err: fmt.Errorf("boom: session index unreadable")}
	src := NewCanonicalFallbackSource("/proj", store)

	metas, err := src.ListSessions(ctx)
	require.Error(t, err, "a canonical read failure must be reported, not silently reported as zero sessions")
	assert.Nil(t, metas)
	assert.Contains(t, err.Error(), "boom: session index unreadable")
}

// countingStore counts every read of the session store. Each of these methods
// enumerates or reads sidecars in the production Manager, so the count is a
// direct proxy for filesystem reads.
type countingStore struct {
	*sessions.MemStore
	reads int
}

func (c *countingStore) ListAll() ([]sessions.Entry, error) {
	c.reads++
	return c.MemStore.ListAll()
}

func (c *countingStore) Find(harpName string) (*sessions.Entry, error) {
	c.reads++
	return c.MemStore.Find(harpName)
}

func (c *countingStore) ListForProject(projectDir string) ([]sessions.Entry, error) {
	c.reads++
	return c.MemStore.ListForProject(projectDir)
}

// listSessionsIndexReads builds a project with n canonical-backed sessions and
// returns how many index reads one ListSessions costs.
func listSessionsIndexReads(t *testing.T, n int) int {
	t.Helper()
	testsupport.Isolate(t)

	store := &countingStore{MemStore: sessions.NewMemStore()}
	for i := 0; i < n; i++ {
		harp := fmt.Sprintf("harp-count-%d", i)
		mintBoundHarp(t, store.MemStore, harp, "/proj", fmt.Sprintf("backend-uuid-c%d", i))
		writeCanonicalFixture(t, harp, "claude-code", "payload")
	}

	src := NewCanonicalFallbackSource("/proj", store)
	store.reads = 0
	metas, err := src.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, metas, n)
	return store.reads
}

// TestCanonicalFallbackSource_ListSessions_IndexReadsDoNotScale pins that
// the dedup set was built by calling store.Find once per canonical session, and
// every Find re-reads and re-parses the entire index file. Listing a project
// with N sessions cost N index reads on top of the enumeration — work that is
// wholly avoidable, since one read already carries every entry. The cost of a
// listing must not depend on how many sessions the project has.
func TestCanonicalFallbackSource_ListSessions_IndexReadsDoNotScale(t *testing.T) {
	var few, many int
	t.Run("one session", func(t *testing.T) { few = listSessionsIndexReads(t, 1) })
	t.Run("six sessions", func(t *testing.T) { many = listSessionsIndexReads(t, 6) })

	assert.Equal(t, few, many,
		"index reads grew with the session count: %d for 1 session, %d for 6", few, many)
}

// TestCanonicalFallbackSource_GetSession_PreservesCallerIDAcrossBothPaths pins
// goofy-dingo defect 1: whichever leg resolves the session, the returned
// Session.ID must be the id the CALLER passed.
//
// Resolving a backend-native id through the reverse index means asking the
// canonical source for the HARP, and the session that comes back is keyed by
// that harp. Letting it out unchanged silently re-keys everything downstream:
// Compactor keys the essence it writes off session.ID, so recover_session then
// reads back under the id it passed and finds nothing. A correct, fully paid-for
// distillation becomes unreadable the instant it is written — exit 0, a success
// message, and an essence nobody can address.
//
// The existing HarpFirstDoesNotBreakSessionIDPath test drives this same leg but
// asserts only the payload, which is why the defect survived. Assert the id.
func TestCanonicalFallbackSource_GetSession_PreservesCallerIDAcrossBothPaths(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	const harp = "harp-identity-kept"
	const vendorID = "12b623a9-b883-4ded-a058-73aba1d1c53c"

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, harp, "/proj", vendorID)
	writeCanonicalFixture(t, harp, "claude-code", "IDENTITY-PAYLOAD")

	src := NewCanonicalFallbackSource("/proj", store)

	t.Run("resolved via the sessionID->harp reverse lookup", func(t *testing.T) {
		sess, err := src.GetSession(ctx, vendorID)
		require.NoError(t, err)
		require.Len(t, sess.Entries, 1)
		assert.Equal(t, "IDENTITY-PAYLOAD", sess.Entries[0].Content, "the right session must still be found")
		assert.Equal(t, vendorID, sess.ID,
			"the caller addressed this session by %q; resolving through harp %q is an implementation detail and must not change the identity that comes back", vendorID, harp)
	})

	t.Run("resolved directly as a harp", func(t *testing.T) {
		sess, err := src.GetSession(ctx, harp)
		require.NoError(t, err)
		require.Len(t, sess.Entries, 1)
		assert.Equal(t, harp, sess.ID, "a caller addressing the session by harp must get the harp back")
	})
}

// TestCanonicalFallbackSource_GetSession_SpecificFirstErrorSurvives pins
// goofy-dingo defect 2: when no leg resolves, the FIRST attempt's error is the
// answer, because it is the specific one.
//
// A NoCanonicalTranscriptError names the harp AND the concrete remedy
// (the vendor-transcript import). Replacing it with a generic message would
// tell the caller nothing about what to do.
func TestCanonicalFallbackSource_GetSession_SpecificFirstErrorSurvives(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	// A harp with no captured transcript, addressed directly: the first leg
	// fails with the specific error and the reverse lookup matches nothing.
	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-uncaptured", "/proj", "")
	src := NewCanonicalFallbackSource("/proj", store)

	_, err := src.GetSession(ctx, "harp-uncaptured")
	require.Error(t, err)

	var uncaptured *NoCanonicalTranscriptError
	require.ErrorAs(t, err, &uncaptured,
		"the specific first-attempt error must reach the caller, not be replaced by a generic one")
	assert.Equal(t, "harp-uncaptured", uncaptured.Harp, "and it must still name the harp the remedy applies to")
}
