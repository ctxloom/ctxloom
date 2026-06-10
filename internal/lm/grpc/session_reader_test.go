package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/ctxloom/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionReader_GetSession(t *testing.T) {
	mock := &MockClient{
		GetSessionFunc: func(_ context.Context, id string) (*agent.Session, error) {
			return &agent.Session{ID: id}, nil
		},
	}
	r := NewSessionReaderWithFactory("gemini", 0, MockClientFactory(mock))

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
	r := NewSessionReaderWithFactory("gemini", 0, MockClientFactory(mock))

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
	r := NewSessionReaderWithFactory("gemini", 0, MockClientFactory(mock))

	got, err := r.CurrentSession(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got, "empty store yields nil session, nil error")
	assert.Equal(t, 0, mock.GetSessionCalls, "no fetch when the store is empty")
}

func TestSessionReader_DialFailureWrapped(t *testing.T) {
	factory := func(string, string, int) (Client, error) { return nil, errors.New("spawn failed") }
	r := NewSessionReaderWithFactory("gemini", 0, factory)

	_, err := r.GetSession(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start gemini plugin")
}

func TestSessionReader_TearsDownOnRPCError(t *testing.T) {
	mock := &MockClient{
		GetSessionFunc: func(context.Context, string) (*agent.Session, error) {
			return nil, errors.New("rpc boom")
		},
	}
	r := NewSessionReaderWithFactory("gemini", 0, MockClientFactory(mock))

	_, err := r.GetSession(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 1, mock.KillCalls, "plugin must be torn down even when the RPC errors")
}
