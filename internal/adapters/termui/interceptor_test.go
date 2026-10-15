package termui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

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
	}, time.Now)
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

// TestInterceptor_ReplyLongerThanTheReadIsDeliveredWhole pins that a terminal
// reply routed to the engine from under a viewer reaches it whole even when
// it completes in a read shorter than itself and the caller's buffer is
// smaller still: the excess waits for the next Read, never dropped.
func TestInterceptor_ReplyLongerThanTheReadIsDeliveredWhole(t *testing.T) {
	h := newHarness(string([]byte{testPrefix})+"j", "\x1b[12;4", "0R")
	var out bytes.Buffer
	buf := make([]byte, 3)
	for {
		n, err := h.ic.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	assert.Equal(t, "\x1b[12;40R", out.String())
	assert.Equal(t, "j", h.ui.String())
}

// TestInterceptor_DisengageDropsAPartialViewerToken pins that a key sequence
// the viewer was still collecting when it closed does not complete in the
// engine: its first bytes went nowhere, so its tail must not arrive as keys.
func TestInterceptor_DisengageDropsAPartialViewerToken(t *testing.T) {
	h := newHarness(string([]byte{testPrefix}) + "j\x1b[1;5")
	assert.Empty(t, h.drain(t))
	h.ic.Disengage()
	h.ic.src = &chunkReader{chunks: [][]byte{[]byte("5")}}
	assert.Equal(t, "5", h.drain(t), "the engine gets what follows, parsed from ground")
	h.ic.mu.Lock()
	defer h.ic.mu.Unlock()
	assert.True(t, h.ic.tok.atBoundary(false))
}

// The prefix in the encodings a terminal sends once the engine has turned on
// an enhanced keyboard mode (claude-code pushes kitty flags and sets
// modifyOtherKeys 2): the interceptor must engage on them exactly as on the
// C0 byte, or Ctrl+] silently reaches the engine and no viewer ever opens.
const (
	kittyPrefix = "\x1b[93;5u"
	motkPrefix  = "\x1b[27;5;93~"
)

func TestInterceptor_EnhancedEncodingsEngage(t *testing.T) {
	for _, enc := range []string{kittyPrefix, "\x1b[93;5:1u", "\x1b[93:125;5u", motkPrefix} {
		h := newHarness("ab" + enc + "j\x1b[106;5u")
		assert.Equal(t, "ab", h.drain(t), "%q: the prefix never reaches the engine", enc)
		assert.Equal(t, 1, h.engaged, "%q engages the viewer", enc)
		assert.Equal(t, "j\x1b[106;5u", h.ui.String(), "%q: the keys after it go to the viewer, whole", enc)
	}
}

func TestInterceptor_EnhancedDoublePressIsOneLiteral(t *testing.T) {
	h := newHarness(kittyPrefix + kittyPrefix + "xyz")
	assert.Equal(t, kittyPrefix+"xyz", h.drain(t), "the second press reaches the engine as it was sent")
	assert.Zero(t, h.engaged)

	h = newHarness(motkPrefix, "\x1d"+"cd")
	assert.Equal(t, "\x1dcd", h.drain(t), "encodings may mix: the same key, pressed twice")
	assert.Equal(t, 1, h.engaged)
	assert.Equal(t, 1, h.aborted)
}

// TestInterceptor_EnhancedPrefixSplitAcrossReads pins that an encoding torn by
// a read boundary is still recognised: a tail that could yet become the
// prefix is held, never half-delivered to the engine.
func TestInterceptor_EnhancedPrefixSplitAcrossReads(t *testing.T) {
	h := newHarness("ab\x1b[93", ";5", "uj")
	assert.Equal(t, "ab", h.drain(t))
	assert.Equal(t, 1, h.engaged)
	assert.Equal(t, "j", h.ui.String())
}

// TestInterceptor_SimilarKeysPassByteIdentical: everything that is not the
// prefix key reaches the engine exactly as the terminal sent it, split reads
// included.
func TestInterceptor_SimilarKeysPassByteIdentical(t *testing.T) {
	similar := []string{
		"\x1b[91;5u", "\x1b[93u", "\x1b[93;3u", "\x1b[93;7u", "\x1b[27;3;93~", "\x1b[27;5;91~",
		"\x1b[1;5A", "\x1b[<0;10;5M", "\x1b[?5u", "\x1b[15~", "\x1bOP", "\x1b[200~pasted\x1b[93;5u\x1b[201~", "é",
	}
	payload := strings.Join(similar, "")
	h := newHarness(payload)
	assert.Equal(t, payload, h.drain(t))
	assert.Zero(t, h.engaged)

	// The same bytes torn at every boundary still arrive identical.
	var chunks []string
	for i := range len(payload) {
		chunks = append(chunks, payload[i:i+1])
	}
	h = newHarness(chunks...)
	assert.Equal(t, payload, h.drain(t), "byte-at-a-time reads")
	assert.Zero(t, h.engaged)
}

// TestInterceptor_UnheldTailIsNotDelayed pins that only a tail that could
// still become the prefix waits for the next read: a lone Esc, or Alt+[,
// reaches the engine in the read it arrived in.
func TestInterceptor_UnheldTailIsNotDelayed(t *testing.T) {
	// "\x1b[9=" and "\x1b[9 " are incomplete CSI sequences no key encoding
	// spells: a private-marker byte or an intermediate after the digits.
	for _, tail := range []string{"\x1b", "\x1b[", "\x1bO", "\x1b[9=", "\x1b[9 "} {
		h := newHarness("a"+tail, "z")
		buf := make([]byte, 64)
		n, err := h.ic.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, "a"+tail, string(buf[:n]), "%q is delivered in its own read", tail)
		assert.Equal(t, "a"+tail+"z", "a"+tail+h.drain(t), "%q: and never repeated", tail)
	}
}

// TestInterceptor_PrefixReleaseIsDropped: with kitty event reporting on, the
// prefix's release follows its press. The engine never saw the press, so it
// must not see the release; and in the fresh state the release is not the
// "other key" that commits to the viewer — a second press is still literal.
func TestInterceptor_PrefixReleaseIsDropped(t *testing.T) {
	const release = "\x1b[93;5:3u"
	h := newHarness(kittyPrefix + release + kittyPrefix + release + "x")
	assert.Equal(t, kittyPrefix+"x", h.drain(t))
	assert.Zero(t, h.engaged)
}

// TestInterceptor_OffReleasesAHeldTail: degrading while a possible prefix is
// held must not lose it.
func TestInterceptor_OffReleasesAHeldTail(t *testing.T) {
	h := newHarness("\x1b[93")
	_, err := h.ic.Read(make([]byte, 64))
	require.ErrorIs(t, err, io.EOF)
	h.ic.Off()
	h.ic.src = &chunkReader{chunks: [][]byte{[]byte(";5u")}}
	assert.Equal(t, "\x1b[93;5u", h.drain(t), "pure passthrough, held bytes first")
	assert.Zero(t, h.engaged)
}

// TestInterceptor_OffDropsTheViewersTail: a tail the VIEWER was collecting
// when the machine went off was never the engine's, so it is dropped rather
// than sent ahead of the next read the way a passthrough tail is.
func TestInterceptor_OffDropsTheViewersTail(t *testing.T) {
	h := newHarness("\x1dj\x1b[9")
	_, err := h.ic.Read(make([]byte, 64))
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 1, h.engaged)
	h.ic.Off()
	h.ic.src = &chunkReader{chunks: [][]byte{[]byte("x")}}
	assert.Equal(t, "x", h.drain(t), "none of the viewer's tail reaches the engine")
}

// TestInterceptor_EveryPrefixShapedTailIsHeld pins holdable's character
// classes at their edges: a tail torn anywhere inside an encoding of the
// prefix — after its first digit, on a '0', a '9', a ':' or a ';', and at the
// longest tail held (maxHeldTail) — waits for the read that completes it, and
// the prefix engages.
func TestInterceptor_EveryPrefixShapedTailIsHeld(t *testing.T) {
	longest := "\x1b[0000093:0000125:0000093;000005" // kitty, the key fields at csiNum's seven digits
	require.Len(t, longest, maxHeldTail)
	for _, split := range [][2]string{
		{"\x1b[9", "3;5u"},     // shortest held tail
		{"\x1b[0", "093;5u"},   // '0' opening the parameters
		{"\x1b[93;0", "5u"},    // '0' inside them
		{"\x1b[27;5;9", "3~"},  // '9' inside them (modifyOtherKeys)
		{"\x1b[93:1", "25;5u"}, // ':' (kitty's shifted alternate)
		{longest, "u"},         // exactly maxHeldTail
	} {
		h := newHarness("ab"+split[0], split[1]+"j")
		assert.Equal(t, "ab", h.drain(t), "%q|%q: no byte of the prefix reaches the engine", split[0], split[1])
		assert.Equal(t, 1, h.engaged, "%q|%q engages", split[0], split[1])
		assert.Equal(t, "j", h.ui.String(), "%q|%q", split[0], split[1])
	}
}

// TestInterceptor_TailBeyondMaxHeldIsNotDelayed: one byte past maxHeldTail is
// no longer held, even when it is still prefix-shaped.
func TestInterceptor_TailBeyondMaxHeldIsNotDelayed(t *testing.T) {
	tail := "\x1b[0000093:0000125:0000093;0000005"
	require.Len(t, tail, maxHeldTail+1)
	h := newHarness(tail, "u")
	buf := make([]byte, 64)
	n, err := h.ic.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, tail, string(buf[:n]), "delivered in its own read")
	assert.Equal(t, "u", h.drain(t))
	assert.Zero(t, h.engaged)
}

// TestInterceptor_TailInAPasteIsNotHeld: pasted text is never the prefix, so
// a prefix-shaped tail inside a paste reaches the engine in its own read.
func TestInterceptor_TailInAPasteIsNotHeld(t *testing.T) {
	h := newHarness("\x1b[200~ab\x1b[93", ";5u\x1b[201~")
	buf := make([]byte, 64)
	n, err := h.ic.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "\x1b[200~ab\x1b[93", string(buf[:n]))
	assert.Equal(t, ";5u\x1b[201~", h.drain(t))
	assert.Zero(t, h.engaged)
}

// TestInterceptor_PartlyForwardedTailIsNotHeld: once part of a tail has gone
// to the engine (it could not yet be the prefix), what follows it is
// forwarded read by read too — the token can no longer be the prefix, so
// nothing waits for it to complete.
func TestInterceptor_PartlyForwardedTailIsNotHeld(t *testing.T) {
	h := newHarness("a\x1b[", "9", ";5u")
	buf := make([]byte, 64)
	for _, want := range []string{"a\x1b[", "9", ";5u"} {
		n, err := h.ic.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, want, string(buf[:n]), "each read's bytes in that read")
	}
	assert.Empty(t, h.drain(t))
	assert.Zero(t, h.engaged)
}

// TestInterceptor_HalfForwardedPrefixIsNotThePrefix: an encoding whose head
// already reached the engine (forwarded before it was prefix-shaped) is not
// the prefix when it completes — the engine has half of it, so it gets the
// rest, and nothing engages.
func TestInterceptor_HalfForwardedPrefixIsNotThePrefix(t *testing.T) {
	h := newHarness("ab\x1b[", "93;5uj")
	assert.Equal(t, "ab\x1b[93;5uj", h.drain(t))
	assert.Zero(t, h.engaged)
	assert.Empty(t, h.ui.String())
}
