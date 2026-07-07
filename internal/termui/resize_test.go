package termui

import (
	"testing"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recvSize(t *testing.T, ch <-chan *pb.WindowSize) *pb.WindowSize {
	t.Helper()
	select {
	case ws, ok := <-ch:
		require.True(t, ok, "size channel closed unexpectedly")
		return ws
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a size event")
		return nil
	}
}

func TestResizeTranslator_ReservesRows_InitialAndSigwinch(t *testing.T) {
	src := make(chan *pb.WindowSize, 4)
	var sizes [][2]int
	rt := NewResizeTranslator(src, 1, func(rows, cols int) { sizes = append(sizes, [2]int{rows, cols}) })

	src <- &pb.WindowSize{Rows: 24, Cols: 80} // watchResize's initial emit
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint32(23), ws.Rows, "engine PTY is rows−N")
	assert.Equal(t, uint32(80), ws.Cols)

	src <- &pb.WindowSize{Rows: 40, Cols: 120} // SIGWINCH
	ws = recvSize(t, rt.Out())
	assert.Equal(t, uint32(39), ws.Rows)
	assert.Equal(t, uint32(120), ws.Cols)

	assert.Equal(t, [][2]int{{24, 80}, {40, 120}}, sizes,
		"the surround sees every REAL size, translation only touches the engine")

	rows, cols := rt.Current()
	assert.Equal(t, 40, rows)
	assert.Equal(t, 120, cols)
	close(src)
}

func TestResizeTranslator_TinyTerminalForwardsUntranslated(t *testing.T) {
	src := make(chan *pb.WindowSize, 1)
	rt := NewResizeTranslator(src, 1, nil)
	src <- &pb.WindowSize{Rows: 5, Cols: 80} // below minRowsForReserve
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint32(5), ws.Rows, "no reservation below the threshold — matches the surround's predicate")
	close(src)
}

func TestResizeTranslator_ZeroReserveIsIdentity(t *testing.T) {
	src := make(chan *pb.WindowSize, 1)
	rt := NewResizeTranslator(src, 0, nil)
	src <- &pb.WindowSize{Rows: 24, Cols: 80}
	ws := recvSize(t, rt.Out())
	assert.Equal(t, uint32(24), ws.Rows)
	close(src)
}

func TestResizeTranslator_NudgeWigglesWithinEngineViewport(t *testing.T) {
	src := make(chan *pb.WindowSize, 1)
	rt := NewResizeTranslator(src, 1, nil)
	src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = recvSize(t, rt.Out()) // drain the initial translated size

	rt.Nudge()
	first := recvSize(t, rt.Out())
	second := recvSize(t, rt.Out())
	assert.Equal(t, uint32(22), first.Rows,
		"wiggle one row SMALLER than the engine viewport (same-size TIOCSWINSZ raises no SIGWINCH)")
	assert.Equal(t, uint32(23), second.Rows, "then settle on the true engine size")
	close(src)
}

func TestResizeTranslator_NudgeBeforeAnySizeIsNoop(t *testing.T) {
	src := make(chan *pb.WindowSize)
	rt := NewResizeTranslator(src, 1, nil)
	rt.Nudge()
	select {
	case ws := <-rt.Out():
		t.Fatalf("unexpected size event %v before any real size", ws)
	case <-time.After(50 * time.Millisecond):
	}
	close(src)
}

func TestResizeTranslator_OutClosesWithSource(t *testing.T) {
	src := make(chan *pb.WindowSize)
	rt := NewResizeTranslator(src, 1, nil)
	close(src) // watchResize closes on ctx done
	select {
	case _, ok := <-rt.Out():
		assert.False(t, ok, "out must close so the client's resize pump ends cleanly")
	case <-time.After(2 * time.Second):
		t.Fatal("out did not close after src closed")
	}
}
