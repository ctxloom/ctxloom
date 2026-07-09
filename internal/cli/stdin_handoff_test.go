package cli

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readResult carries one Read's outcome across a goroutine boundary.
type readResult struct {
	data []byte
	err  error
}

// readAsync starts a Read on r and returns the channel its result lands on.
func readAsync(r io.Reader) <-chan readResult {
	ch := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := r.Read(buf)
		ch <- readResult{data: append([]byte(nil), buf[:n]...), err: err}
	}()
	return ch
}

// waitResult receives a readResult or fails the test after a generous timeout.
func waitResult(t *testing.T, ch <-chan readResult) readResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not complete in time")
		return readResult{}
	}
}

func TestStdinHandoff_DeliversDataToActiveLease(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	h := newStdinHandoff(pr)
	lease := h.Attach()

	resCh := readAsync(lease)
	_, err := pw.Write([]byte("hello"))
	require.NoError(t, err)

	res := waitResult(t, resCh)
	require.NoError(t, res.err)
	assert.Equal(t, "hello", string(res.data))
}

// The verified failure mode: the run's stdin pump is parked in Read when the
// run ends; the relaunch prompt then reads stdin and its first answer line
// vanishes into the pump. With the handoff, detaching the pump's lease (a)
// unblocks its parked Read with io.EOF without consuming anything and (b)
// routes the in-flight read's bytes to the next lease — the prompt.
func TestStdinHandoff_DetachHandsInFlightReadToNextLease(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	h := newStdinHandoff(pr)

	pump := h.Attach()
	pumpCh := readAsync(pump) // parks: no data yet (one source read in flight)

	// Run ends; the cmd layer detaches the pump before prompting.
	pump.Detach()
	res := waitResult(t, pumpCh)
	assert.Equal(t, io.EOF, res.err, "detached lease's parked Read must return EOF")
	assert.Empty(t, res.data, "detached lease must not consume bytes")

	// The user answers the prompt; the in-flight source read completes now.
	prompt := h.Attach()
	promptCh := readAsync(prompt)
	_, err := pw.Write([]byte("y\n"))
	require.NoError(t, err)

	got := waitResult(t, promptCh)
	require.NoError(t, got.err)
	assert.Equal(t, "y\n", string(got.data), "the answer must reach the prompt, not the dead pump")
}

func TestStdinHandoff_DetachedLeaseReadsEOFImmediately(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	h := newStdinHandoff(pr)

	lease := h.Attach()
	lease.Detach()

	n, err := lease.Read(make([]byte, 8))
	assert.Zero(t, n)
	assert.Equal(t, io.EOF, err)
}

// Bytes buffered before a detach stay queued for the next lease instead of
// being claimed by the detached one.
func TestStdinHandoff_PendingBytesSurviveDetach(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	h := newStdinHandoff(pr)

	first := h.Attach()
	firstCh := readAsync(first)
	_, err := pw.Write([]byte("abc"))
	require.NoError(t, err)
	res := waitResult(t, firstCh)
	require.NoError(t, res.err)
	require.Equal(t, "abc", string(res.data))

	// Park another read, feed it data and detach concurrently-ish: either the
	// read returns the data (raced first) or EOF (detach won) — but the data
	// must never be lost.
	first.Detach()
	second := h.Attach()
	secondCh := readAsync(second)
	_, err = pw.Write([]byte("def"))
	require.NoError(t, err)
	got := waitResult(t, secondCh)
	require.NoError(t, got.err)
	assert.Equal(t, "def", string(got.data))
}

func TestStdinHandoff_SourceErrorSurfacesAfterPendingDrains(t *testing.T) {
	pr, pw := io.Pipe()
	h := newStdinHandoff(pr)
	lease := h.Attach()

	resCh := readAsync(lease)
	require.NoError(t, pw.CloseWithError(io.ErrUnexpectedEOF))

	res := waitResult(t, resCh)
	assert.Empty(t, res.data)
	assert.Equal(t, io.ErrUnexpectedEOF, res.err)

	// The error is sticky for subsequent leases too.
	next := h.Attach()
	_, err := next.Read(make([]byte, 8))
	assert.Equal(t, io.ErrUnexpectedEOF, err)
}
