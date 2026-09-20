package coord

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// recordingStream is the runner's RunChannel stand-in: it records the seq of
// every event frame in the order the frames reached the wire.
type recordingStream struct {
	grpc.ClientStream
	mu   sync.Mutex
	seqs []uint64
}

func (s *recordingStream) Send(f *agentcoordpb.AgentFrame) error {
	if ev := f.GetEvent(); ev != nil {
		s.mu.Lock()
		s.seqs = append(s.seqs, ev.GetSeq())
		s.mu.Unlock()
	}
	return nil
}

func (s *recordingStream) Recv() (*agentcoordpb.CoordinatorFrame, error) {
	select {}
}

// TestHome_ConcurrentEmittersReachTheWireInSeqOrder pins the invariant the
// cumulative Ack depends on: the coordinator dedupes a plane-1 event by "seq
// at or below the channel's watermark", so an event that reaches the wire
// BEHIND a higher seq is dropped as a duplicate — and its ack releases a
// Report whose fact was never journaled (the roster's empty summary, the
// artifact download's NotFound). Seq assignment and the stream write are
// therefore one critical section: many emitters at once, one order on the
// wire.
func TestHome_ConcurrentEmittersReachTheWireInSeqOrder(t *testing.T) {
	h := newNoticeHome(t)
	stream := &recordingStream{}
	h.stream = stream

	const emitters, perEmitter = 32, 16
	var wg sync.WaitGroup
	for i := 0; i < emitters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perEmitter; j++ {
				h.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Summary{Summary: &agentcoordpb.Summary{Text: "x"}}})
			}
		}()
	}
	wg.Wait()

	stream.mu.Lock()
	defer stream.mu.Unlock()
	require.Len(t, stream.seqs, emitters*perEmitter)
	for i := 1; i < len(stream.seqs); i++ {
		require.Greater(t, stream.seqs[i], stream.seqs[i-1],
			"event seq %d reached the wire behind seq %d (index %d): the coordinator drops it as a duplicate and acks past it",
			stream.seqs[i], stream.seqs[i-1], i)
	}
}
