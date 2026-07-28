package termui

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPrefix byte = 0x1d // ctrl-]

// chunkReader returns one predefined chunk per Read call, then EOF — the
// hermetic stand-in for the raw tty, letting tests control read-boundary
// placement exactly.
type chunkReader struct{ chunks [][]byte }

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks[0] = c.chunks[0][n:]
	if len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

// ixHarness wires an interceptor to recording callbacks and drains it.
type ixHarness struct {
	ic      *interceptor
	engaged int
	aborted int
	ui      bytes.Buffer
	// engageSink is returned by the Engage callback; nil simulates a failed
	// viewer start.
	engageSink func() io.Writer
}

func newHarness(chunks ...string) *ixHarness {
	h := &ixHarness{}
	h.engageSink = func() io.Writer { return &h.ui }
	cr := &chunkReader{}
	for _, c := range chunks {
		cr.chunks = append(cr.chunks, []byte(c))
	}
	h.ic = newInterceptor(cr, testPrefix, InterceptorCallbacks{
		Engage: func() io.Writer {
			h.engaged++
			return h.engageSink()
		},
		AbortLiteral: func() { h.aborted++ },
	})
	return h
}

// drain reads until EOF, concatenating everything the engine would receive.
func (h *ixHarness) drain(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 64)
	for {
		n, err := h.ic.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			return out.String()
		}
	}
}

func TestInterceptor_PassthroughPurity(t *testing.T) {
	// Byte-exact passthrough incl. control chars, escape sequences, and utf8.
	payload := "plain text\r\n\x1b[2J\x1b[1;1H\ttabs\x00nul → ünïcode"
	h := newHarness(payload)
	assert.Equal(t, payload, h.drain(t))
	assert.Zero(t, h.engaged)
	assert.Zero(t, h.aborted)
	assert.Zero(t, h.ui.Len())
}

func TestInterceptor_PassthroughZeroCopy(t *testing.T) {
	// The hot path must return the caller's buffer slice untouched: one read,
	// no intermediate copies.
	h := newHarness("abcdef")
	buf := make([]byte, 16)
	n, _ := h.ic.Read(buf)
	assert.Equal(t, "abcdef", string(buf[:n]))
}

func TestInterceptor_PrefixEngages(t *testing.T) {
	h := newHarness("abc\x1d")
	assert.Equal(t, "abc", h.drain(t))
	assert.Equal(t, 1, h.engaged, "prefix at chunk end engages the viewer")
	assert.Zero(t, h.ui.Len())
}

func TestInterceptor_DoublePressSameChunk_LiteralNoFlash(t *testing.T) {
	h := newHarness("\x1d\x1dxyz")
	assert.Equal(t, "\x1dxyz", h.drain(t), "exactly one literal prefix byte, then passthrough")
	assert.Zero(t, h.engaged, "a same-chunk double press must never open the viewer")
	assert.Zero(t, h.aborted)
}

func TestInterceptor_DoublePressAcrossChunks_LiteralAborts(t *testing.T) {
	h := newHarness("\x1d", "\x1dcd")
	assert.Equal(t, "\x1dcd", h.drain(t))
	assert.Equal(t, 1, h.engaged, "first chunk ends in prefix → engage fires")
	assert.Equal(t, 1, h.aborted, "second prefix aborts the engagement with the literal")
}

func TestInterceptor_UIRouting(t *testing.T) {
	h := newHarness("\x1djk", "xq")
	assert.Equal(t, "", h.drain(t), "viewer keys never reach the engine")
	assert.Equal(t, 1, h.engaged)
	assert.Equal(t, "jkxq", h.ui.String())
}

func TestInterceptor_MixedChunk_SplitsAtPrefix(t *testing.T) {
	h := newHarness("hello\x1dj")
	assert.Equal(t, "hello", h.drain(t))
	assert.Equal(t, 1, h.engaged)
	assert.Equal(t, "j", h.ui.String())
}

func TestInterceptor_DisengageResumesPassthrough(t *testing.T) {
	h := newHarness("\x1dj")
	_ = h.drain(t)
	require.Equal(t, 1, h.engaged)

	h.ic.Disengage()
	cr := &chunkReader{chunks: [][]byte{[]byte("back to engine")}}
	h.ic.src = cr
	assert.Equal(t, "back to engine", h.drain(t))
	assert.Equal(t, "j", h.ui.String(), "no further viewer bytes after disengage")
}

func TestInterceptor_ReengageAfterDisengage(t *testing.T) {
	h := newHarness("\x1da")
	_ = h.drain(t)
	h.ic.Disengage()

	h.ic.src = &chunkReader{chunks: [][]byte{[]byte("\x1db")}}
	_ = h.drain(t)
	assert.Equal(t, 2, h.engaged)
	assert.Equal(t, "ab", h.ui.String())
}

func TestInterceptor_EngageFailureDropsToPassthrough(t *testing.T) {
	h := newHarness("\x1dj", "typed after")
	h.engageSink = func() io.Writer { return nil } // viewer failed to start
	assert.Equal(t, "typed after", h.drain(t),
		"after a failed engage the stream returns to passthrough")
	assert.Equal(t, 1, h.engaged)
	assert.Zero(t, h.ui.Len(), "keys aimed at the failed viewer are dropped, not leaked to the engine")
}

func TestInterceptor_OffForwardsPrefix(t *testing.T) {
	h := newHarness("\x1dab\x1d\x1d")
	h.ic.Off()
	assert.Equal(t, "\x1dab\x1d\x1d", h.drain(t), "degraded mode is pure passthrough, prefix included")
	assert.Zero(t, h.engaged)
}

func TestInterceptor_LiteralThenEngage_SameChunk(t *testing.T) {
	// literal pair, engine text, then a fresh engagement with a viewer key.
	h := newHarness("\x1d\x1dab\x1dj")
	assert.Equal(t, "\x1dab", h.drain(t))
	assert.Equal(t, 1, h.engaged)
	assert.Equal(t, "j", h.ui.String())
}

func TestInterceptor_AbortThenReengage_AcrossChunks(t *testing.T) {
	// Engaged in chunk 1; chunk 2 aborts with a literal, types, re-engages.
	h := newHarness("\x1d", "\x1dab\x1dk")
	assert.Equal(t, "\x1dab", h.drain(t))
	assert.Equal(t, 2, h.engaged)
	assert.Equal(t, 1, h.aborted)
	assert.Equal(t, "k", h.ui.String())
}

func TestInterceptor_ReadNeverReturnsZeroNil(t *testing.T) {
	// A chunk fully consumed by the state machine (a lone prefix) must not
	// surface as (0, nil) — the interceptor reads on until it has engine
	// bytes or an error. The follow-up abort-literal chunk provides them.
	h := newHarness("\x1d", "\x1drest")
	buf := make([]byte, 64)
	n, err := h.ic.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "\x1drest", string(buf[:n]))
	assert.Equal(t, 1, h.engaged)
	assert.Equal(t, 1, h.aborted)
}
