package runner

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// eofStream is a RunChannel whose every write fails the way grpc reports any
// transport write error: a bare io.EOF.
type eofStream struct{ grpc.ClientStream }

func (eofStream) Send(*agentcoordpb.AgentFrame) error { return io.EOF }

func (eofStream) Recv() (*agentcoordpb.CoordinatorFrame, error) { select {} }

// TestHome_SendFailureWarnsOnlyWhileTheHomeIsLive pins that a failed send is
// only a "reconnecting" warning while something will reconnect: once the
// home's context has ended the stream died with it, and the warning would be
// teardown noise.
func TestHome_SendFailureWarnsOnlyWhileTheHomeIsLive(t *testing.T) {
	frame := &agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Heartbeat{Heartbeat: &agentcoordpb.Heartbeat{}}}

	t.Run("ending home is silent", func(t *testing.T) {
		var found report.Collector
		h := testHome(t)
		h.rep = report.To(&found)
		h.stream = eofStream{}
		h.cancel()

		assert.False(t, h.trySend(frame), "the frame still did not reach the stream")
		assert.Empty(t, found.All())
	})

	t.Run("live home warns once", func(t *testing.T) {
		var found report.Collector
		h := testHome(t)
		h.rep = report.To(&found)
		h.stream = eofStream{}

		assert.False(t, h.trySend(frame))
		assert.Len(t, found.All(), 1)
	})
}
