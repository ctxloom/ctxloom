package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type noHistoryBackend struct{ agent.Backend }

func (noHistoryBackend) Name() string                  { return "nohist" }
func (noHistoryBackend) History() agent.SessionHistory { return nil }

// The Chat handler already answers a missing capability with
// codes.Unimplemented and a client protocol violation with
// codes.InvalidArgument (U059-F04), because a bare fmt.Errorf reaches the
// caller as codes.Unknown — indistinguishable from "the transport died". The
// session-history and Run handlers on the same service answered every one of
// those the second way. A host that cannot tell "this backend has no session
// history" from "the plugin crashed" cannot decide whether retrying, falling
// back to another reader, or reporting a hard failure is right.
func TestServerHandlers_ClassifyRefusalsInsteadOfCodesUnknown(t *testing.T) {
	client, _ := plugin.TestPluginGRPCConn(t, false, map[string]plugin.Plugin{
		LLMPluginKey: &LLMGRPCPlugin{Impl: noHistoryBackend{}},
	})
	t.Cleanup(func() { _ = client.Close() })

	raw, err := client.Dispense(LLMPluginKey)
	require.NoError(t, err)
	c := raw.(*GRPCClient)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("GetSession on a backend with no history", func(t *testing.T) {
		_, err := c.GetSession(ctx, "s1")
		require.NotErrorIs(t, err, context.DeadlineExceeded, "the RPC parked instead of answering")
		require.Error(t, err)
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})

	t.Run("ListSessions on a backend with no history", func(t *testing.T) {
		_, err := c.ListSessions(ctx)
		require.NotErrorIs(t, err, context.DeadlineExceeded, "the RPC parked instead of answering")
		require.Error(t, err)
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})

	t.Run("WatchSession on a backend with no history", func(t *testing.T) {
		events, errs, err := c.WatchSession(ctx, "s1")
		require.NoError(t, err, "the stream opens; the refusal arrives on it")
		for range events { //nolint:revive // draining to closure is the wait
		}
		select {
		case werr := <-errs:
			require.Error(t, werr)
			assert.Equal(t, codes.Unimplemented, status.Code(werr))
		case <-ctx.Done():
			t.Fatal("the watch stream never reported why it ended")
		}
	})
}

// An absent session is NOT a missing capability: a host that has a history
// reader and simply cannot find this id must be able to say so.
func TestGetSession_AbsentSessionIsNotFound(t *testing.T) {
	client, _ := plugin.TestPluginGRPCConn(t, false, map[string]plugin.Plugin{
		LLMPluginKey: &LLMGRPCPlugin{Impl: nilHistoryBackend{}},
	})
	t.Cleanup(func() { _ = client.Close() })

	raw, err := client.Dispense(LLMPluginKey)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = raw.(*GRPCClient).GetSession(ctx, "no-such-session")
	require.NotErrorIs(t, err, context.DeadlineExceeded, "the RPC parked instead of answering")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// A first Run frame that carries no start is the CLIENT's protocol violation,
// exactly as it is on Chat (chat.go answers that with codes.InvalidArgument
// under U059-F04). Reported as codes.Unknown it is indistinguishable from the
// transport dying mid-handshake.
func TestRun_FirstMessageMustCarryStart_IsInvalidArgument(t *testing.T) {
	srv := &GRPCServer{Impl: &fakeBackend{name: "claude-code"}}
	stream := newFakeRunServer()
	stream.recv = []*RunInput{{Input: &RunInput_Stdin{Stdin: []byte("no start")}}}

	err := srv.Run(stream)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
