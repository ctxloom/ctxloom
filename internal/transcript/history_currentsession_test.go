package transcript

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestCurrentSession_SkipsAnUnreadableCandidate pins CurrentSession against
// the same store state ListSessions already survives.
//
// CurrentSession took the FIRST project harp carrying a canonical transcript
// path and returned whatever GetSession said about it, error included. So one
// unreadable entry — a file corrupted mid-write, a schema version this build
// refuses, a permissions change — made "current session" fail outright, while
// ListSessions over the identical store cheerfully returned every other
// session. Same data, two answers, and the failing one is the path a resume
// picker and the memory compactor take.
func TestCurrentSession_SkipsAnUnreadableCandidate(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()
	const projectDir = "/proj/skip-unreadable"

	good := "codex-fixture-harp"
	installFixture(t, "codex", good)

	broken := "broken-newest-harp"
	writeTempTranscript(t, broken, []byte("not json at all\nstill not json\n"))

	store := sessions.NewMemStore()
	mint(t, store, good, projectDir)
	time.Sleep(2 * time.Millisecond) // the broken one is the MOST recent
	mint(t, store, broken, projectDir)

	h := NewCanonicalHistory(projectDir, store)

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	// ListSessions already survives this state; that is the baseline, and it
	// also proves the fixture is genuinely hostile (the broken harp is
	// excluded because it FAILED to parse, not because it was never seen).
	metas, err := h.ListSessions(ctx)
	require.NoError(t, err)
	require.Len(t, metas, 1, "the readable session is still listed")
	assert.Equal(t, good, metas[0].ID)
	require.Contains(t, sink.String(), broken, "fixture is not hostile: the broken harp parsed fine")

	cur, err := h.CurrentSession(ctx)
	require.NoError(t, err, "one unreadable entry must not fail the whole lookup")
	require.NotNil(t, cur, "the next readable candidate is the current session")
	assert.Equal(t, good, cur.ID)
}

// TestCurrentSession_AllCandidatesUnreadableStillErrors pins the other edge:
// skipping is for getting PAST a bad entry to a good one, not for converting a
// project whose every transcript is unreadable into a clean "no sessions".
// That distinction is the whole point — "the file was unreadable"
// and "the conversation was empty" are different facts — and the caller
// (lm/grpc's CanonicalFallbackSource) routes on exactly it.
func TestCurrentSession_AllCandidatesUnreadableStillErrors(t *testing.T) {
	testsupport.Isolate(t)
	const projectDir = "/proj/all-broken"

	first := "broken-one-harp"
	second := "broken-two-harp"
	writeTempTranscript(t, first, []byte("garbage\n"))
	writeTempTranscript(t, second, []byte("garbage\n"))

	store := sessions.NewMemStore()
	mint(t, store, first, projectDir)
	time.Sleep(2 * time.Millisecond)
	mint(t, store, second, projectDir)

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	cur, err := NewCanonicalHistory(projectDir, store).CurrentSession(context.Background())
	require.Error(t, err, "every candidate unreadable is a failure, not an empty state")
	assert.Nil(t, cur)
}
