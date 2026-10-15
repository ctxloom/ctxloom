package termui

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pasteLeftDraining engages the viewer, begins a paste inside it, and closes
// the viewer mid-paste: the rest of that paste is the interceptor's to drop.
func pasteLeftDraining(t *testing.T) *ixHarness {
	t.Helper()
	h := newHarness("\x1dj\x1b[200~abc")
	_, err := h.ic.Read(make([]byte, 64))
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 1, h.engaged)
	h.ic.Disengage()
	require.True(t, h.ic.draining, "a paste the closed viewer began is drained")
	return h
}

// TestInterceptor_ADrainedPasteNeverReachesTheEngine: the rest of a paste the
// closed viewer began is dropped up to its end marker — a read of plain paste
// text (no ESC, no prefix byte: what the passthrough fast path would take), a
// read ending on a torn end marker — and only what follows the marker is the
// engine's.
func TestInterceptor_ADrainedPasteNeverReachesTheEngine(t *testing.T) {
	h := pasteLeftDraining(t)
	h.ic.src = &chunkReader{chunks: [][]byte{[]byte("more paste"), []byte("tail\x1b[20"), []byte("1~x")}}
	assert.Equal(t, "x", h.drain(t))
}

// TestInterceptor_OffWhileDrainingDropsTheTornEndMarker: degrading while a
// paste drains drops its torn tail with it; nothing of the paste goes out
// ahead of the next read.
func TestInterceptor_OffWhileDrainingDropsTheTornEndMarker(t *testing.T) {
	h := pasteLeftDraining(t)
	h.ic.src = &chunkReader{chunks: [][]byte{[]byte("tail\x1b[20")}}
	assert.Empty(t, h.drain(t))
	h.ic.Off()
	h.ic.src = &chunkReader{chunks: [][]byte{[]byte("z")}}
	assert.Equal(t, "z", h.drain(t))
}

// TestInterceptor_OffReleasesAHeldTailOnce: the tail held for the prefix when
// the machine went off goes out ahead of the next read — that read only.
func TestInterceptor_OffReleasesAHeldTailOnce(t *testing.T) {
	h := newHarness("\x1b[93")
	_, err := h.ic.Read(make([]byte, 64))
	require.ErrorIs(t, err, io.EOF)
	h.ic.Off()
	h.ic.src = &chunkReader{chunks: [][]byte{[]byte(";5u"), []byte("z")}}
	assert.Equal(t, "\x1b[93;5uz", h.drain(t))
}

// readEach asserts each Read returns exactly the next of want.
func readEach(t *testing.T, h *ixHarness, want ...string) {
	t.Helper()
	buf := make([]byte, 64)
	for _, w := range want {
		n, err := h.ic.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, w, string(buf[:n]))
	}
}

// TestInterceptor_TailsNoPrefixCanCompleteAreNotHeld pins holdable's guards
// one at a time: a tail is held only if it is ESC '[' then a digit, outside a
// paste. Each tail below is carried (an incomplete sequence) but breaks
// exactly one of those, and reaches the engine in the read it arrived in.
func TestInterceptor_TailsNoPrefixCanCompleteAreNotHeld(t *testing.T) {
	for _, c := range []struct {
		name   string
		chunks []string
	}{
		{"OSC: ']' where '[' must be", []string{"a\x1b]9", "\x07"}},
		{"a byte below '0' after CSI", []string{"a\x1b[/", "u"}},
		{"a private marker after CSI", []string{"a\x1b[?", "5u"}},
		{"a torn end marker inside a paste", []string{"\x1b[200~ab\x1b[20", "1~"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(c.chunks...)
			readEach(t, h, c.chunks...)
			assert.Zero(t, h.engaged)
		})
	}
}
