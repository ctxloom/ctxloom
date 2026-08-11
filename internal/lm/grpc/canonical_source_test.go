package grpc

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// fakeSessionSource is a minimal in-memory pb.SessionSource stand-in for the
// legacy leg of CanonicalFallbackSource — the per-engine scraper reader these
// tests must prove is bypassed whenever a canonical transcript exists, and
// consulted whenever it doesn't.
type fakeSessionSource struct {
	sessions map[string]*agent.Session
	metas    []agent.SessionMeta
	current  *agent.Session
	getErr   error
	listErr  error
}

func (f *fakeSessionSource) GetSession(_ context.Context, id string) (*agent.Session, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	sess, ok := f.sessions[id]
	if !ok {
		return nil, fmt.Errorf("fake legacy source: no session %q", id)
	}
	return sess, nil
}

func (f *fakeSessionSource) ListSessions(_ context.Context) ([]agent.SessionMeta, error) {
	return f.metas, f.listErr
}

func (f *fakeSessionSource) CurrentSession(_ context.Context) (*agent.Session, error) {
	return f.current, nil
}

// writeCanonicalFixture records one real assistant turn (via the actual
// transcript.Recorder writer, not hand-rolled JSONL) to harp's canonical
// transcript path, so these tests exercise the real writer/reader contract.
func writeCanonicalFixture(t *testing.T, harp, engine, content string) {
	t.Helper()
	rec, err := transcript.NewRecorder(harp, engine)
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
	writeCanonicalFixture(t, harp, "codex", "about to be clobbered")
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(`{"v":9999,"ts":"2026-01-01T00:00:00Z"}`+"\n"), 0o600))
}

// TestCanonicalFallbackSource_GetSession_NoLegacy_CorruptCanonicalSurfaces
// pins that the FIRST canonical read's error was discarded outright. For a
// retired-scraper backend (legacy == nil) that turned "your transcript is
// corrupt and I refuse to guess at it" into "there is no canonical transcript
// for this session" — the one message that tells the user to stop looking.
func TestCanonicalFallbackSource_GetSession_NoLegacy_CorruptCanonicalSurfaces(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-corrupt", "/proj", "backend-uuid-9")
	writeCorruptCanonicalFixture(t, "harp-corrupt")

	src := NewCanonicalFallbackSource(nil, "/proj", store)

	_, err := src.GetSession(ctx, "harp-corrupt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version", "the real cause must reach the caller")
	assert.NotContains(t, err.Error(), "no canonical transcript",
		"an unreadable transcript is not an absent one")
}

// TestCanonicalFallbackSource_GetSession_NoLegacy_AbsentCanonicalStaysAbsent is
// the other half: a genuinely uncaptured session must still report absence, not
// a transcript-read failure. Surfacing the first error unconditionally would
// have turned every miss into a scary parse error.
func TestCanonicalFallbackSource_GetSession_NoLegacy_AbsentCanonicalStaysAbsent(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-uncaptured", "/proj", "backend-uuid-10")

	src := NewCanonicalFallbackSource(nil, "/proj", store)

	_, err := src.GetSession(ctx, "harp-uncaptured")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no canonical transcript")
}

// mintBoundHarp registers harp under projectDir in store with the given
// bound backend session id — AssignHarp mints its own name, so Rename
// retargets it to the exact name the test wants (mirrors internal/transcript's
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
// canonical — the legacy source is wired to fail the test if consulted at
// all, so this also proves canonical genuinely short-circuits legacy rather
// than merely winning a race.
func TestCanonicalFallbackSource_GetSession_PrefersCanonical(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-canonical", "/proj", "backend-uuid-1")
	writeCanonicalFixture(t, "harp-canonical", "codex", "REAL-CANONICAL-PAYLOAD")

	legacy := &fakeSessionSource{getErr: fmt.Errorf("legacy GetSession must not be called when canonical exists")}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	sess, err := src.GetSession(ctx, "backend-uuid-1")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "REAL-CANONICAL-PAYLOAD", sess.Entries[0].Content, "payload must survive the round trip, not just a non-empty session")
}

// TestCanonicalFallbackSource_GetSession_FallsBackWhenNoCanonical proves the
// transitional half of the selection rule: a harp that predates capture (no
// canonical transcript ever written) is served from the legacy source
// unchanged.
func TestCanonicalFallbackSource_GetSession_FallsBackWhenNoCanonical(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-legacy-only", "/proj", "backend-uuid-2")
	// Deliberately no writeCanonicalFixture call for this harp.

	legacySess := &agent.Session{
		ID:      "backend-uuid-2",
		Entries: []agent.SessionEntry{{Type: agent.EntryTypeAssistant, Content: "REAL-LEGACY-PAYLOAD"}},
	}
	legacy := &fakeSessionSource{sessions: map[string]*agent.Session{"backend-uuid-2": legacySess}}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	sess, err := src.GetSession(ctx, "backend-uuid-2")
	require.NoError(t, err)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "REAL-LEGACY-PAYLOAD", sess.Entries[0].Content)
}

// TestCanonicalFallbackSource_GetSession_ResolvesHarpDirectly proves that
// `memory list`'s SESSION ID column literally displays the
// HARP for a canonical-backed session (CanonicalHistory.ListSessions sets
// meta.ID = harp, never the backend-native session id), so `memory show
// <that harp>` must resolve — not just the never-surfaced backend-native id
// the old sessionID-only reverse lookup required. The legacy source is wired
// to fail the test if consulted at all, proving the harp resolves via the
// canonical leg directly, without ever needing the index's reverse
// SessionID->harp lookup.
func TestCanonicalFallbackSource_GetSession_ResolvesHarpDirectly(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-direct", "/proj", "backend-uuid-direct")
	writeCanonicalFixture(t, "harp-direct", "codex", "DIRECT-HARP-PAYLOAD")

	legacy := &fakeSessionSource{getErr: fmt.Errorf("legacy GetSession must not be called when the harp resolves directly")}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

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
	writeCanonicalFixture(t, "harp-canonical-2", "codex", "STILL-WORKS-PAYLOAD")

	legacy := &fakeSessionSource{getErr: fmt.Errorf("legacy GetSession must not be called when canonical exists")}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	sess, err := src.GetSession(ctx, "backend-uuid-still-works")
	require.NoError(t, err)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "STILL-WORKS-PAYLOAD", sess.Entries[0].Content)
}

// TestCanonicalFallbackSource_GetSession_UnboundIDFallsBack covers a session
// id the index has no entry for at all (e.g. --session <uuid> pasted from a
// legacy `memory list` row) — resolveHarp finds nothing, so this must still
// reach the legacy source directly by id, exactly like pre-S4 behavior.
func TestCanonicalFallbackSource_GetSession_UnboundIDFallsBack(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	legacySess := &agent.Session{ID: "unbound-uuid", Entries: []agent.SessionEntry{{Type: agent.EntryTypeAssistant, Content: "UNBOUND-PAYLOAD"}}}
	legacy := &fakeSessionSource{sessions: map[string]*agent.Session{"unbound-uuid": legacySess}}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	sess, err := src.GetSession(ctx, "unbound-uuid")
	require.NoError(t, err)
	assert.Equal(t, "UNBOUND-PAYLOAD", sess.Entries[0].Content)
}

// TestCanonicalFallbackSource_CurrentSession_PrefersCanonical mirrors the
// GetSession proof for the no-explicit-id "current session" path.
func TestCanonicalFallbackSource_CurrentSession_PrefersCanonical(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-current", "/proj", "backend-uuid-3")
	writeCanonicalFixture(t, "harp-current", "codex", "CURRENT-CANONICAL-PAYLOAD")

	legacy := &fakeSessionSource{current: &agent.Session{ID: "should-not-be-used"}}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	sess, err := src.CurrentSession(ctx)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "harp-current", sess.ID)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, "CURRENT-CANONICAL-PAYLOAD", sess.Entries[0].Content)
}

// TestCanonicalFallbackSource_CurrentSession_FallsBackWhenProjectHasNoCanonical
// proves a project with no canonical-backed session at all (pre-capture
// project) degrades to the legacy source's own "current" pick.
func TestCanonicalFallbackSource_CurrentSession_FallsBackWhenProjectHasNoCanonical(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	legacy := &fakeSessionSource{current: &agent.Session{ID: "legacy-current", Entries: []agent.SessionEntry{{Type: agent.EntryTypeAssistant, Content: "LEGACY-CURRENT"}}}}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	sess, err := src.CurrentSession(ctx)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "legacy-current", sess.ID)
}

// TestCanonicalFallbackSource_ListSessions_MergesAndDedupes proves the
// listing merge: a canonical-backed harp appears once (from canonical, keyed
// by harp), and a legacy session NOT covered by any canonical harp still
// appears (keyed by its own backend-native id) — the transitional decay the
// plan describes, in one listing.
func TestCanonicalFallbackSource_ListSessions_MergesAndDedupes(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-listed", "/proj", "backend-uuid-covered")
	writeCanonicalFixture(t, "harp-listed", "codex", "LISTED-PAYLOAD")

	legacy := &fakeSessionSource{metas: []agent.SessionMeta{
		{ID: "backend-uuid-covered"}, // same session as harp-listed: must be deduped away
		{ID: "backend-uuid-uncovered"},
	}}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	metas, err := src.ListSessions(ctx)
	require.NoError(t, err)

	ids := make(map[string]bool, len(metas))
	for _, m := range metas {
		ids[m.ID] = true
	}
	assert.True(t, ids["harp-listed"], "canonical-backed session must be listed by harp")
	assert.False(t, ids["backend-uuid-covered"], "the same session must not also appear under its legacy id")
	assert.True(t, ids["backend-uuid-uncovered"], "a legacy session with no canonical counterpart must still be listed")
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

// TestCanonicalFallbackSource_ListSessions_NoLegacy_CanonicalErrorPropagates
// pins that legacy==nil is the retired-scraper case (codex, kiro,
// antigravity, claude-code — S5, canonical is the ONLY source). Before the
// fix, `canonMetas, _ := f.canonical.ListSessions(ctx)` discarded the error
// and returned (nil, nil) — a confident "no sessions" indistinguishable from
// a project that genuinely has none. With no legacy leg to degrade to, the
// canonical failure IS the failure and must be reported.
func TestCanonicalFallbackSource_ListSessions_NoLegacy_CanonicalErrorPropagates(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := &erroringListStore{MemStore: sessions.NewMemStore(), err: fmt.Errorf("boom: session index unreadable")}
	src := NewCanonicalFallbackSource(nil, "/proj", store)

	metas, err := src.ListSessions(ctx)
	require.Error(t, err, "a canonical read failure with no legacy leg to fall back to must be reported, not silently reported as zero sessions")
	assert.Nil(t, metas)
	assert.Contains(t, err.Error(), "boom: session index unreadable")
}

// TestCanonicalFallbackSource_ListSessions_CanonicalErrorsButLegacyHasSessions
// is the companion case: legacy != nil and legacy succeeds, so the existing
// degrade-to-what-succeeded behavior (already exercised in the mirror
// direction by MergesAndDedupes/legacy-fails) must still return the legacy
// listing rather than erroring the whole call — a canonical failure must not
// regress a still-transitional project that has a working legacy leg.
func TestCanonicalFallbackSource_ListSessions_CanonicalErrorsButLegacyHasSessions(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	store := &erroringListStore{MemStore: sessions.NewMemStore(), err: fmt.Errorf("boom: session index unreadable")}
	legacy := &fakeSessionSource{metas: []agent.SessionMeta{{ID: "legacy-only-session"}}}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

	metas, err := src.ListSessions(ctx)
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, "legacy-only-session", metas[0].ID)
}

// countingStore counts every read of the session index. Each of these methods
// re-reads and re-parses the whole index file in the production Manager, so the
// count is a direct proxy for file reads.
type countingStore struct {
	*sessions.MemStore
	reads int
}

func (c *countingStore) Load() (*sessions.Index, error) {
	c.reads++
	return c.MemStore.Load()
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
		writeCanonicalFixture(t, harp, "codex", "payload")
	}

	src := NewCanonicalFallbackSource(&fakeSessionSource{}, "/proj", store)
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

// TestRetiredScraperRoster_IsClosedAndImmutable pins that the roster used
// to be an exported package-level MAP, so every importer could add or remove a
// backend from it at run time — silently deciding, for the whole process,
// whether some engine gets a legacy scraper leg. The accessors expose exactly
// the two questions callers actually ask, and neither hands out a writable view
// of the package's own copy.
//
// A red for this could not be shown: the defect is the shape of the API, so the
// test only compiles once the accessors exist.
func TestRetiredScraperRoster_IsClosedAndImmutable(t *testing.T) {
	assert.Equal(t, []string{"claude-code", "codex", "kiro"}, RetiredScraperBackendNames())

	for _, name := range RetiredScraperBackendNames() {
		assert.True(t, IsRetiredScraperBackend(name), "%s must be reported as retired", name)
	}
	assert.False(t, IsRetiredScraperBackend("opencode"),
		"opencode's native reader is correct and keeps its legacy leg")
	assert.False(t, IsRetiredScraperBackend(""))

	// A caller reordering or truncating what it was handed cannot reach the
	// roster itself.
	got := RetiredScraperBackendNames()
	got[0] = "tampered"
	assert.Equal(t, []string{"claude-code", "codex", "kiro"}, RetiredScraperBackendNames())
	assert.False(t, IsRetiredScraperBackend("tampered"))
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

	legacy := &fakeSessionSource{getErr: fmt.Errorf("legacy GetSession must not be called when canonical exists")}
	src := NewCanonicalFallbackSource(legacy, "/proj", store)

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
// (the vendor-transcript import). It is set aside during selection as a
// "try the other leg" signal, which is right — but discarding it at the END
// replaced an actionable message with a generic "legacy scraper reader retired"
// that tells the caller nothing about what to do.
func TestCanonicalFallbackSource_GetSession_SpecificFirstErrorSurvives(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	// A harp with no captured transcript, addressed directly, on a
	// retired-scraper backend (legacy == nil): the first leg fails with the
	// specific error and the reverse lookup matches nothing.
	store := sessions.NewMemStore()
	mintBoundHarp(t, store, "harp-uncaptured", "/proj", "")
	src := NewCanonicalFallbackSource(nil, "/proj", store)

	_, err := src.GetSession(ctx, "harp-uncaptured")
	require.Error(t, err)

	var uncaptured *transcript.NoCanonicalTranscriptError
	require.ErrorAs(t, err, &uncaptured,
		"the specific first-attempt error must reach the caller, not be replaced by a generic one")
	assert.Equal(t, "harp-uncaptured", uncaptured.Harp, "and it must still name the harp the remedy applies to")
}
