package transcript

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// entryTrackingRecorder is a test double that FAILS (via t.Errorf, from
// whichever goroutine catches the violation) if Record is ever entered by a
// second caller while a prior call is still "in flight". It widens the
// critical section with a short sleep so any actual concurrent-caller
// violation is caught reliably rather than by luck. This is what pins
// outer-petal at the unit level: GRPCClient.Chat used to call
// transcript.RecordUserText (-> Record) from its inbound tap goroutine and
// Tee's Record (via TeeAndClose) from a SEPARATE outbound goroutine, with
// nothing coordinating the two beyond fileRecorder's own mutex — which
// prevents data corruption but does nothing to make record ORDER reflect a
// coordinated policy rather than raw lock-acquisition scheduling.
type entryTrackingRecorder struct {
	t         *testing.T
	active    int32
	violation int32 // set non-zero if two calls ever overlapped
	mu        sync.Mutex
	recorded  []agent.ChatEvent
}

func (r *entryTrackingRecorder) Record(ev agent.ChatEvent) error {
	if atomic.AddInt32(&r.active, 1) != 1 {
		atomic.StoreInt32(&r.violation, 1)
	}
	time.Sleep(2 * time.Millisecond) // widen the window
	atomic.AddInt32(&r.active, -1)

	r.mu.Lock()
	r.recorded = append(r.recorded, ev)
	r.mu.Unlock()
	return nil
}

func (r *entryTrackingRecorder) Close() error { return nil }

// TestCoordinatedRecorder_SingleOwnerAcrossProducers pins the outer-petal fix:
// a CoordinatedRecorder must be the ONLY caller into the wrapped Recorder,
// regardless of how many producer goroutines call Submit concurrently — this
// is what "coordinate the two goroutines" (the taskloom outer-petal wording)
// means in code: no producer ever calls Record directly, so there is exactly
// one owner of the seq counter's call sequence, not two independent ones
// racing a shared mutex.
func TestCoordinatedRecorder_SingleOwnerAcrossProducers(t *testing.T) {
	rec := &entryTrackingRecorder{t: t}
	const producers = 8
	const perProducer = 20
	cr := NewCoordinatedRecorder(rec, producers)

	ctx := context.Background()

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			defer cr.ProducerDone()
			for i := 0; i < perProducer; i++ {
				cr.Submit(ctx, agent.ChatEvent{Entry: &agent.SessionEntry{
					Type:    agent.EntryTypeUser,
					Content: "msg",
				}})
			}
		}(p)
	}
	wg.Wait()
	<-cr.Done()

	assert.Equal(t, int32(0), atomic.LoadInt32(&rec.violation),
		"Record was entered concurrently by more than one caller — the recorder is not coordinated to a single owner")

	// Payload assertion: every submitted event actually landed — coordination
	// must not drop records under concurrent load.
	require.Len(t, rec.recorded, producers*perProducer,
		"not every submitted ChatEvent reached the wrapped Recorder")
}

// TestCoordinatedRecorder_ClosesWrappedRecorderOnceAllProducersDone proves the
// wrapped Recorder is closed exactly once, after every registered producer
// has finished — mirroring TeeAndClose's contract (S2's host seams have no
// other "the chat is over" signal).
func TestCoordinatedRecorder_ClosesWrappedRecorderOnceAllProducersDone(t *testing.T) {
	rec := &closeTrackingRecorder{}
	cr := NewCoordinatedRecorder(rec, 2)

	cr.Submit(context.Background(), agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "a"}})
	cr.ProducerDone()

	select {
	case <-cr.Done():
		t.Fatal("Recorder closed before every producer finished")
	case <-time.After(20 * time.Millisecond):
	}

	cr.ProducerDone()
	select {
	case <-cr.Done():
	case <-time.After(time.Second):
		t.Fatal("Recorder was never closed after the last producer finished")
	}
	assert.Equal(t, 1, rec.closes, "wrapped Recorder.Close must be called exactly once")
}

type closeTrackingRecorder struct {
	mu     sync.Mutex
	closes int
}

func (r *closeTrackingRecorder) Record(agent.ChatEvent) error { return nil }
func (r *closeTrackingRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}
