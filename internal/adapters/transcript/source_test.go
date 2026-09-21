package transcript

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// storeHistory is an agent.SessionHistory over a canned store: the sessions
// by id, the listing, and the workDir every read was scoped to.
type storeHistory struct {
	agent.SessionHistory
	sessions map[string]*agent.Session
	metas    []agent.SessionMeta
	err      error
	workDirs []string
}

func (h *storeHistory) GetSession(workDir, id string) (*agent.Session, error) {
	h.workDirs = append(h.workDirs, workDir)
	if h.err != nil {
		return nil, h.err
	}
	return h.sessions[id], nil
}

func (h *storeHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	h.workDirs = append(h.workDirs, workDir)
	if h.err != nil {
		return nil, h.err
	}
	return h.metas, nil
}

func TestEngineReader_GetSession_ScopedToTheProject(t *testing.T) {
	hist := &storeHistory{sessions: map[string]*agent.Session{"sess-1": {ID: "sess-1"}}}
	r := NewEngineReader(hist, "/proj")

	got, err := r.GetSession(context.Background(), "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-1", got.ID)
	assert.Equal(t, []string{"/proj"}, hist.workDirs, "the read is scoped to the reader's project")
}

func TestEngineReader_GetSession_UnknownIDIsAnError(t *testing.T) {
	r := NewEngineReader(&storeHistory{sessions: map[string]*agent.Session{}}, "/proj")
	_, err := r.GetSession(context.Background(), "nope")
	var missing *NoSessionError
	require.ErrorAs(t, err, &missing, "an absent session is reported, never a nil session")
	assert.Equal(t, "nope", missing.ID)
}

func TestEngineReader_CurrentSession_ResolvesMostRecent(t *testing.T) {
	hist := &storeHistory{
		metas:    []agent.SessionMeta{{ID: "newest"}, {ID: "older"}},
		sessions: map[string]*agent.Session{"newest": {ID: "newest"}, "older": {ID: "older"}},
	}
	r := NewEngineReader(hist, "/proj")

	got, err := r.CurrentSession(context.Background())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "newest", got.ID, "current = most-recent listed")
}

func TestEngineReader_CurrentSession_Empty(t *testing.T) {
	r := NewEngineReader(&storeHistory{}, "/proj")
	got, err := r.CurrentSession(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got, "empty store yields nil session, nil error")
}

func TestEngineReader_NoHistoryRefusesEveryRead(t *testing.T) {
	r := NewEngineReader(nil, "/proj")
	_, err := r.GetSession(context.Background(), "x")
	require.ErrorIs(t, err, ErrNoHistory)
	_, err = r.ListSessions(context.Background())
	require.ErrorIs(t, err, ErrNoHistory)
	_, _, err = r.WatchSession(context.Background(), "x")
	require.ErrorIs(t, err, ErrNoHistory)
}

func TestEngineReader_StoreErrorIsReturned(t *testing.T) {
	r := NewEngineReader(&storeHistory{err: errors.New("store boom")}, "/proj")
	_, err := r.GetSession(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "store boom")
}

func TestEngineReader_WatchSession_StreamsTheStore(t *testing.T) {
	hist := &storeHistory{sessions: map[string]*agent.Session{"s1": {ID: "s1", Entries: []agent.SessionEntry{{Content: "a"}}}}}
	r := NewEngineReader(hist, "/proj")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, errs, err := r.WatchSession(ctx, "s1")
	require.NoError(t, err)
	first := <-events
	require.NotNil(t, first.Entry)
	assert.Equal(t, "a", first.Entry.Content)
	cancel()
	for range events {
	}
	assert.NoError(t, <-errs)
}
