package agentbus

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenTestBus(t *testing.T, b *Broker) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "bus.sock")
	srv, err := Listen(sock, b)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestSocket_SendRecvRoundTrip drives the forwarded path end to end over a
// real Unix socket: a child's ForwardSend lands in the parent's mailbox; the
// parent's local Recv sees it; a coordinator Send completes the child's
// parked ForwardRecv.
func TestSocket_SendRecvRoundTrip(t *testing.T) {
	b := New(Hooks{})
	b.AddChild("child-a", "coord")
	sock := listenTestBus(t, b)

	require.NoError(t, ForwardSend(sock, "child-a", ParentAddress, "result", "all done"))
	msgs, err := b.Recv(context.Background(), "coord", time.Second)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "all done", msgs[0].Body)
	assert.Equal(t, "result", msgs[0].Kind)
	assert.Equal(t, "child-a", msgs[0].From)

	got := make(chan []Message, 1)
	go func() {
		m, rerr := ForwardRecv(sock, "child-a", 5*time.Second)
		assert.NoError(t, rerr)
		got <- m
	}()
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, parked := b.parked["child-a"]
		return parked
	}, 2*time.Second, 5*time.Millisecond)
	b.Send(Message{From: "coord", To: "child-a", Body: "follow-up"})
	select {
	case m := <-got:
		require.Len(t, m, 1)
		assert.Equal(t, "follow-up", m[0].Body)
	case <-time.After(2 * time.Second):
		t.Fatal("forwarded recv never completed")
	}
}

// TestSocket_TypedErrorsCrossTheWire pins that the typed failures survive the
// forward: peer routing, unknown sender, and the recv timeout all map back to
// their sentinels client-side.
func TestSocket_TypedErrorsCrossTheWire(t *testing.T) {
	b := New(Hooks{})
	b.AddChild("child-a", "coord")
	b.AddChild("child-b", "coord")
	sock := listenTestBus(t, b)

	err := ForwardSend(sock, "child-a", "child-b", "", "psst")
	require.ErrorIs(t, err, ErrPeerRouting)

	err = ForwardSend(sock, "stranger", ParentAddress, "", "hi")
	require.ErrorIs(t, err, ErrUnknownSender)

	_, err = ForwardRecv(sock, "child-a", 20*time.Millisecond)
	require.ErrorIs(t, err, ErrRecvTimeout)
}

// TestListen_ReplacesStaleSocket pins recovery from a dead predecessor's
// leftover socket file.
func TestListen_ReplacesStaleSocket(t *testing.T) {
	b := New(Hooks{})
	sock := filepath.Join(t.TempDir(), "bus.sock")
	srv1, err := Listen(sock, b)
	require.NoError(t, err)
	require.NoError(t, srv1.Close())

	srv2, err := Listen(sock, b)
	require.NoError(t, err)
	defer srv2.Close()

	b.AddChild("child-a", "coord")
	require.NoError(t, ForwardSend(sock, "child-a", ParentAddress, "", "alive"))
}
