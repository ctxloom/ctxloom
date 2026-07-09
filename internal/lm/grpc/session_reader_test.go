package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionReader_GetSession(t *testing.T) {
	mock := &MockClient{
		GetSessionFunc: func(_ context.Context, id string) (*agent.Session, error) {
			return &agent.Session{ID: id}, nil
		},
	}
	r := NewSessionReaderWithFactory("antigravity", 0, MockClientFactory(mock))

	got, err := r.GetSession(context.Background(), "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-1", got.ID)
	assert.Equal(t, 1, mock.GetSessionCalls)
	assert.Equal(t, 1, mock.KillCalls, "the plugin must be torn down after the read")
}

func TestSessionReader_ListSessions(t *testing.T) {
	mock := &MockClient{
		ListSessionsFunc: func(context.Context) ([]agent.SessionMeta, error) {
			return []agent.SessionMeta{{ID: "a"}, {ID: "b"}}, nil
		},
	}
	r := NewSessionReaderWithFactory("claude-code", 0, MockClientFactory(mock))

	got, err := r.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].ID)
	assert.Equal(t, 1, mock.KillCalls)
}

func TestSessionReader_CurrentSession_ResolvesMostRecent(t *testing.T) {
	var fetched string
	mock := &MockClient{
		ListSessionsFunc: func(context.Context) ([]agent.SessionMeta, error) {
			return []agent.SessionMeta{{ID: "newest"}, {ID: "older"}}, nil
		},
		GetSessionFunc: func(_ context.Context, id string) (*agent.Session, error) {
			fetched = id
			return &agent.Session{ID: id}, nil
		},
	}
	r := NewSessionReaderWithFactory("antigravity", 0, MockClientFactory(mock))

	got, err := r.CurrentSession(context.Background())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "newest", got.ID, "current = most-recent listed")
	assert.Equal(t, "newest", fetched)
	assert.Equal(t, 1, mock.KillCalls, "list+get share one plugin lifetime")
}

func TestSessionReader_CurrentSession_Empty(t *testing.T) {
	mock := &MockClient{
		ListSessionsFunc: func(context.Context) ([]agent.SessionMeta, error) { return nil, nil },
	}
	r := NewSessionReaderWithFactory("antigravity", 0, MockClientFactory(mock))

	got, err := r.CurrentSession(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got, "empty store yields nil session, nil error")
	assert.Equal(t, 0, mock.GetSessionCalls, "no fetch when the store is empty")
}

func TestSessionReader_DialFailureWrapped(t *testing.T) {
	factory := func(string, string, int) (Client, error) { return nil, errors.New("spawn failed") }
	r := NewSessionReaderWithFactory("antigravity", 0, factory)

	_, err := r.GetSession(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start antigravity plugin")
}

// closedWatchStream returns pre-filled, already-closed event/error channels, as
// if a short server stream had completed.
func closedWatchStream(events ...*WatchEvent) (<-chan *WatchEvent, <-chan error) {
	ec := make(chan *WatchEvent, len(events))
	for _, e := range events {
		ec <- e
	}
	close(ec)
	errc := make(chan error)
	close(errc)
	return ec, errc
}

func TestSessionReader_WatchSession_StreamsThenTearsDown(t *testing.T) {
	killed := make(chan struct{})
	mock := &MockClient{
		WatchSessionFunc: func(context.Context, string) (<-chan *WatchEvent, <-chan error, error) {
			ec, errc := closedWatchStream(
				&WatchEvent{Event: &WatchEvent_Entry{Entry: &SessionEntry{Content: "a"}}},
				&WatchEvent{Event: &WatchEvent_Boundary{Boundary: &ResponseBoundary{ToIndex: 1}}},
			)
			return ec, errc, nil
		},
		KillFunc: func() { close(killed) },
	}
	r := NewSessionReaderWithFactory("claude-code", 0, MockClientFactory(mock))

	events, errs, err := r.WatchSession(context.Background(), "s1")
	require.NoError(t, err)

	var got []*WatchEvent
	for ev := range events {
		got = append(got, ev)
	}
	require.NoError(t, <-errs)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].GetEntry().GetContent())

	// The plugin is torn down only after the stream ends — assert via the Kill
	// signal rather than racing the teardown goroutine.
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("plugin must be torn down after the watch stream ends")
	}
}

func TestSessionReader_WatchSession_DialFailureWrapped(t *testing.T) {
	factory := func(string, string, int) (Client, error) { return nil, errors.New("spawn failed") }
	r := NewSessionReaderWithFactory("antigravity", 0, factory)

	_, _, err := r.WatchSession(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start antigravity plugin")
}

func TestSessionReader_WatchSession_OpenErrorTearsDown(t *testing.T) {
	mock := &MockClient{
		WatchSessionFunc: func(context.Context, string) (<-chan *WatchEvent, <-chan error, error) {
			return nil, nil, errors.New("open boom")
		},
	}
	r := NewSessionReaderWithFactory("antigravity", 0, MockClientFactory(mock))

	_, _, err := r.WatchSession(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 1, mock.KillCalls, "plugin must be torn down when opening the stream fails")
}

func TestSessionReader_TearsDownOnRPCError(t *testing.T) {
	mock := &MockClient{
		GetSessionFunc: func(context.Context, string) (*agent.Session, error) {
			return nil, errors.New("rpc boom")
		},
	}
	r := NewSessionReaderWithFactory("antigravity", 0, MockClientFactory(mock))

	_, err := r.GetSession(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 1, mock.KillCalls, "plugin must be torn down even when the RPC errors")
}
