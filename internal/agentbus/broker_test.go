package agentbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendFromChild_RoutingPolicy pins the hub-and-spoke policy: a child may
// address "parent" or its parent's harp (both resolve to the parent's
// mailbox); a sibling harp is the typed peer-routing error; an unregistered
// sender is refused.
func TestSendFromChild_RoutingPolicy(t *testing.T) {
	b := New(Hooks{})
	b.AddChild("child-a", "coord")
	b.AddChild("child-b", "coord")

	require.NoError(t, b.SendFromChild("child-a", ParentAddress, "result", "done"))
	require.NoError(t, b.SendFromChild("child-a", "coord", "", "again"))

	err := b.SendFromChild("child-a", "child-b", "", "psst")
	require.ErrorIs(t, err, ErrPeerRouting)

	err = b.SendFromChild("stranger", ParentAddress, "", "hi")
	require.ErrorIs(t, err, ErrUnknownSender)

	msgs, err := b.Recv(context.Background(), "coord", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, "done", msgs[0].Body)
	assert.Equal(t, "child-a", msgs[0].From)
	assert.Equal(t, "coord", msgs[0].To)
}

// TestRecv_PerSenderFIFO pins ordering: messages from one sender arrive in
// send order (the broker is in fact globally FIFO per recipient).
func TestRecv_PerSenderFIFO(t *testing.T) {
	b := New(Hooks{})
	b.AddChild("child-a", "coord")
	for i := 0; i < 5; i++ {
		require.NoError(t, b.SendFromChild("child-a", ParentAddress, "", fmt.Sprintf("msg-%d", i)))
	}
	msgs, err := b.Recv(context.Background(), "coord", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 5)
	for i, m := range msgs {
		assert.Equal(t, fmt.Sprintf("msg-%d", i), m.Body)
	}
}

// TestRecv_TimeoutIsTyped pins the recv contract: an empty mailbox with an
// elapsed wait (or no wait) fails with the typed ErrRecvTimeout — the caller
// drops the coordination; there is no retry tier.
func TestRecv_TimeoutIsTyped(t *testing.T) {
	b := New(Hooks{})
	_, err := b.Recv(context.Background(), "coord", 0)
	require.ErrorIs(t, err, ErrRecvTimeout)

	start := time.Now()
	_, err = b.Recv(context.Background(), "coord", 30*time.Millisecond)
	require.ErrorIs(t, err, ErrRecvTimeout)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}

// TestRecv_ParkAndComplete pins the parked-receive path: a recv with no
// pending mail parks (OnPark fires — the orchestrator's slot yield), a later
// send completes it with the message, and OnUnpark runs before the caller
// resumes (slot re-acquisition happens-before the child continues its turn).
func TestRecv_ParkAndComplete(t *testing.T) {
	var mu sync.Mutex
	var events []string
	hook := func(name string) func(string) {
		return func(harp string) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, name+":"+harp)
		}
	}
	b := New(Hooks{OnPark: hook("park"), OnUnpark: hook("unpark")})
	b.AddChild("child-a", "coord")

	got := make(chan []Message, 1)
	go func() {
		msgs, err := b.Recv(context.Background(), "child-a", 5*time.Second)
		require.NoError(t, err)
		got <- msgs
	}()

	// Wait for the park to register before sending.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) == 1 && events[0] == "park:child-a"
	}, 2*time.Second, 5*time.Millisecond)

	delivered := b.Send(Message{From: "coord", To: "child-a", Body: "follow-up"})
	assert.True(t, delivered, "a parked recv should consume the send")

	select {
	case msgs := <-got:
		require.Len(t, msgs, 1)
		assert.Equal(t, "follow-up", msgs[0].Body)
	case <-time.After(2 * time.Second):
		t.Fatal("parked recv never completed")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"park:child-a", "unpark:child-a"}, events)
}

// TestRecv_TimeoutFiresUnpark pins that an elapsed park still restores the
// slot accounting: OnUnpark runs on the timeout path too.
func TestRecv_TimeoutFiresUnpark(t *testing.T) {
	var mu sync.Mutex
	var unparked []string
	b := New(Hooks{OnUnpark: func(h string) {
		mu.Lock()
		defer mu.Unlock()
		unparked = append(unparked, h)
	}})
	_, err := b.Recv(context.Background(), "child-a", 20*time.Millisecond)
	require.ErrorIs(t, err, ErrRecvTimeout)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"child-a"}, unparked)
}

// TestRecv_SecondConcurrentParkIsBusy pins one-parked-receive-per-harp.
func TestRecv_SecondConcurrentParkIsBusy(t *testing.T) {
	b := New(Hooks{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := b.Recv(context.Background(), "child-a", 500*time.Millisecond)
		assert.ErrorIs(t, err, ErrRecvTimeout)
	}()
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, parked := b.parked["child-a"]
		return parked
	}, 2*time.Second, 5*time.Millisecond)
	_, err := b.Recv(context.Background(), "child-a", 50*time.Millisecond)
	require.ErrorIs(t, err, ErrRecvBusy)
	<-done
}

// TestRecv_CancelledContext pins the park-abandon path on caller
// cancellation.
func TestRecv_CancelledContext(t *testing.T) {
	b := New(Hooks{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := b.Recv(ctx, "child-a", 5*time.Second)
	require.ErrorIs(t, err, context.Canceled)
}

// TestSend_QueuesWhenNotParked pins mid-turn queueing: with no parked recv
// the message stays in the mailbox for the turn-boundary drain (TakeNext).
func TestSend_QueuesWhenNotParked(t *testing.T) {
	b := New(Hooks{})
	delivered := b.Send(Message{From: "coord", To: "child-a", Body: "one"})
	assert.False(t, delivered)
	b.Send(Message{From: "coord", To: "child-a", Body: "two"})

	msg, ok := b.TakeNext("child-a")
	require.True(t, ok)
	assert.Equal(t, "one", msg.Body)
	msg, ok = b.TakeNext("child-a")
	require.True(t, ok)
	assert.Equal(t, "two", msg.Body)
	_, ok = b.TakeNext("child-a")
	assert.False(t, ok)
}

// TestBroker_ConcurrentSendRecv races many senders against a draining
// receiver to exercise the mutex paths under -race; every message must arrive
// exactly once and per-sender order must hold.
func TestBroker_ConcurrentSendRecv(t *testing.T) {
	b := New(Hooks{})
	const senders, perSender = 8, 50
	for i := 0; i < senders; i++ {
		b.AddChild(fmt.Sprintf("child-%d", i), "coord")
	}
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perSender; j++ {
				require.NoError(t, b.SendFromChild(fmt.Sprintf("child-%d", i), ParentAddress, "", fmt.Sprintf("%d:%d", i, j)))
			}
		}(i)
	}
	seen := make(map[string]int) // sender → next expected seq
	total := 0
	deadline := time.After(10 * time.Second)
	for total < senders*perSender {
		msgs, err := b.Recv(context.Background(), "coord", 2*time.Second)
		require.NoError(t, err)
		for _, m := range msgs {
			var si, sj int
			_, serr := fmt.Sscanf(m.Body, "%d:%d", &si, &sj)
			require.NoError(t, serr)
			assert.Equal(t, seen[m.From], sj, "per-sender FIFO violated for %s", m.From)
			seen[m.From]++
			total++
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of %d messages arrived", total, senders*perSender)
		default:
		}
	}
	wg.Wait()
}

// errors.Is sanity for the wire mapping used by socket.go.
func TestTypedErrorsAreSentinels(t *testing.T) {
	assert.True(t, errors.Is(ErrRecvTimeout, ErrRecvTimeout))
	assert.False(t, errors.Is(ErrRecvTimeout, ErrPeerRouting))
}

// TestInjectFromUser_SenderIdentityAndMirror pins S2's two invariants at the
// broker: the injected text reaches the child with sender identity "user" (a
// completed parked recv can tell it wasn't the parent), and the O3 mirror
// notice lands in the parent's mailbox — kind, child harp, digest — on the
// same injection.
func TestInjectFromUser_SenderIdentityAndMirror(t *testing.T) {
	b := New(Hooks{})
	b.AddChild("child-a", "coord")

	got := make(chan []Message, 1)
	go func() {
		msgs, err := b.Recv(context.Background(), "child-a", 5*time.Second)
		assert.NoError(t, err)
		got <- msgs
	}()
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, parked := b.parked["child-a"]
		return parked
	}, 2*time.Second, 5*time.Millisecond)

	completed, err := b.InjectFromUser("child-a", "focus on the lexer first")
	require.NoError(t, err)
	assert.True(t, completed, "the parked recv consumed the injection")

	select {
	case msgs := <-got:
		require.Len(t, msgs, 1)
		assert.Equal(t, UserSender, msgs[0].From, "the child sees the USER as the sender, not its parent")
		assert.Equal(t, "focus on the lexer first", msgs[0].Body)
	case <-time.After(2 * time.Second):
		t.Fatal("parked recv never completed")
	}

	mirror, err := b.Recv(context.Background(), "coord", 0)
	require.NoError(t, err)
	require.Len(t, mirror, 1)
	assert.Equal(t, KindUserInjected, mirror[0].Kind)
	assert.Equal(t, "child-a", mirror[0].From, "the notice names the injected child")
	assert.Equal(t, "focus on the lexer first", mirror[0].Body)
}

// TestInjectFromUser_MirrorOnQueuedDeliveryToo pins the ALWAYS half of O3:
// the mirror fires even when no recv is parked and the text just queues —
// there is no silent-injection path.
func TestInjectFromUser_MirrorOnQueuedDeliveryToo(t *testing.T) {
	b := New(Hooks{})
	b.AddChild("child-a", "coord")

	completed, err := b.InjectFromUser("child-a", "queued text")
	require.NoError(t, err)
	assert.False(t, completed)

	msg, ok := b.TakeNext("child-a")
	require.True(t, ok)
	assert.Equal(t, UserSender, msg.From)
	assert.Equal(t, "queued text", msg.Body)

	mirror, err := b.Recv(context.Background(), "coord", 0)
	require.NoError(t, err)
	require.Len(t, mirror, 1)
	assert.Equal(t, KindUserInjected, mirror[0].Kind)
	assert.Equal(t, "child-a", mirror[0].From)
}

// TestInjectFromUser_UnknownTargetIsTyped pins the refusal: an unregistered
// target is the typed ErrNotInjectable, and a refused injection delivers and
// mirrors nothing.
func TestInjectFromUser_UnknownTargetIsTyped(t *testing.T) {
	b := New(Hooks{})
	_, err := b.InjectFromUser("never-registered", "hello?")
	require.ErrorIs(t, err, ErrNotInjectable)
	_, err = b.Recv(context.Background(), "coord", 0)
	require.ErrorIs(t, err, ErrRecvTimeout, "a refused injection mirrors nothing")
}

// TestInjectDigest_Bounds pins the mirror body bound: short text rides
// verbatim; long text is cut at the rune bound with the total length
// appended so the parent knows how much it didn't see.
func TestInjectDigest_Bounds(t *testing.T) {
	assert.Equal(t, "short", injectDigest("short"))

	long := strings.Repeat("x", 300)
	d := injectDigest(long)
	assert.Equal(t, strings.Repeat("x", injectDigestRunes)+"…", string([]rune(d)[:injectDigestRunes+1]))
	assert.Contains(t, d, "(300 chars total)")
}
