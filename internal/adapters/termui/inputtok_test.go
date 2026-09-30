package termui

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type emitted struct {
	k   tokKind
	tok string
}

func (e emitted) String() string { return fmt.Sprintf("%v:%q", e.k, e.tok) }

// tokenize feeds chunks in order and collects every whole token emitted.
func tokenize(t *testing.T, loneEsc bool, chunks ...string) ([]emitted, *inputTok) {
	t.Helper()
	var tk inputTok
	var got []emitted
	for _, c := range chunks {
		tk.feed([]byte(c), loneEsc, func(k tokKind, b []byte) { got = append(got, emitted{k, string(b)}) })
	}
	return got, &tk
}

// TestInputTok_Classifies pins lock 5's vocabulary: which complete input
// sequences are the TERMINAL answering a query (routed to the engine that
// asked), which are the mouse (dropped under the modal), and which are keys.
func TestInputTok_Classifies(t *testing.T) {
	cases := []struct {
		in   string
		want tokKind
	}{
		{"a", tokKey},
		{"\r", tokKey},
		{"\x1b[A", tokKey},              // arrow
		{"\x1b[1;5C", tokKey},           // ctrl+right
		{"\x1b[97;5u", tokKey},          // kitty key event (no '?')
		{"\x1bOP", tokKey},              // SS3 F1
		{"\x1ba", tokKey},               // alt+a
		{"é", tokKey},                   // one rune
		{"\x1b[12;40R", tokReport},      // CPR
		{"\x1b[?12;40R", tokReport},     // DECXCPR
		{"\x1b[?62;22c", tokReport},     // DA1
		{"\x1b[>41;354;0c", tokReport},  // DA2
		{"\x1b[0n", tokReport},          // DSR
		{"\x1b[?2026;2$y", tokReport},   // DECRPM
		{"\x1b[I", tokReport},           // focus in
		{"\x1b[O", tokReport},           // focus out
		{"\x1b[?1u", tokReport},         // kitty flags reply
		{"\x1b]11;rgb:0000/0000/0000\x1b\\", tokReport}, // OSC 11, ST
		{"\x1b]10;rgb:ffff/ffff/ffff\a", tokReport},     // OSC 10, BEL
		{"\x1bP>|xterm(390)\x1b\\", tokReport},          // XTVERSION (DCS)
		{"\x1b_Gi=1;OK\x1b\\", tokReport},               // kitty graphics (APC)
		{"\x1b[<0;10;5M", tokMouse},     // SGR press
		{"\x1b[<0;10;5m", tokMouse},     // SGR release
		{"\x1b[32;10;5M", tokMouse},     // urxvt
		{"\x1b[M !!", tokMouse},         // X10: CSI M + three raw bytes
	}
	for _, tc := range cases {
		got, tk := tokenize(t, false, tc.in)
		require.Len(t, got, 1, "%q must be ONE token: %v", tc.in, got)
		assert.Equal(t, emitted{tc.want, tc.in}, got[0], "%q", tc.in)
		assert.True(t, tk.atBoundary(false), "%q leaves the stream at ground", tc.in)
	}
}

// TestInputTok_CarriesASplitTokenWhole pins lock 2's premise: a sequence split
// across reads is ONE token, emitted once the read completing it arrives —
// including an OSC reply, whose type is decided by its first bytes.
func TestInputTok_CarriesASplitTokenWhole(t *testing.T) {
	for _, in := range []string{"\x1b[12;40R", "\x1b]11;rgb:0000/0000/0000\x1b\\", "\x1b[<0;10;5M", "\x1b[M !!", "é", "\x1bP>|xterm\x1b\\"} {
		for cut := 1; cut < len(in); cut++ {
			got, tk := tokenize(t, false, in[:cut], in[cut:])
			require.Len(t, got, 1, "%q cut at %d: %v", in, cut, got)
			assert.Equal(t, in, got[0].tok, "%q cut at %d", in, cut)
			assert.True(t, tk.atBoundary(false))
		}
		_, tk := tokenize(t, false, in[:len(in)-1])
		assert.False(t, tk.atBoundary(true), "%q unfinished is never a boundary", in)
	}
}

func TestInputTok_LoneTrailingEsc(t *testing.T) {
	got, tk := tokenize(t, false, "x\x1b")
	assert.Equal(t, []emitted{{tokKey, "x"}}, got, "passthrough tracking carries a trailing ESC: it may begin a sequence")
	assert.False(t, tk.atBoundary(false), "not a boundary while keys may still be arriving")
	assert.True(t, tk.atBoundary(true), "after the quiet period a lone ESC was the Esc key")

	got, _ = tokenize(t, true, "x\x1b")
	assert.Equal(t, []emitted{{tokKey, "x"}, {tokKey, "\x1b"}}, got, "routing treats a read ending in a lone ESC as the Esc key")

	got, _ = tokenize(t, true, "\xc3", "\xa9")
	assert.Equal(t, []emitted{{tokKey, "é"}}, got, "routing emits only a lone ESC early — never half a rune")

	got, _ = tokenize(t, true, "é\xc3", "\xa9")
	assert.Equal(t, []emitted{{tokKey, "é"}, {tokKey, "é"}}, got, "a truncated rune is never folded into the grapheme before it")

	got, _ = tokenize(t, false, "\x1b", "[A")
	assert.Equal(t, []emitted{{tokKey, "\x1b[A"}}, got, "a carried ESC completed by the next read is one sequence")

	_, tk = tokenize(t, true, "\x1b[")
	assert.False(t, tk.atBoundary(false), "an introducer alone is carried, even when routing")
	assert.True(t, tk.atBoundary(true), "and was Alt+[ once the stream is quiet")
}

// TestInputTok_PasteIsOneRegion pins that a bracketed paste — markers,
// content, even an ESC or a CR inside it, across any number of reads, with
// the end marker itself split — is emitted as begin/data/end and holds the
// stream off ground until it ends.
func TestInputTok_PasteIsOneRegion(t *testing.T) {
	got, tk := tokenize(t, false, "a\x1b[200~ls -la\r", "rm \x1b[31mx\x1b[2", "01~b")
	assert.Equal(t, []emitted{
		{tokKey, "a"},
		{tokPasteBegin, "\x1b[200~"},
		{tokPasteData, "ls -la\r"},
		{tokPasteData, "rm \x1b[31mx"},
		{tokPasteEnd, "\x1b[201~"},
		{tokKey, "b"},
	}, got)
	assert.True(t, tk.atBoundary(false))

	_, tk = tokenize(t, false, "\x1b[200~half a paste")
	assert.False(t, tk.atBoundary(true), "inside a paste is never a boundary")
}

// TestInputTok_OversizedTailIsReleased pins that a sequence that never ends
// cannot grow the carry without bound: past the cap it is let go as a key.
func TestInputTok_OversizedTailIsReleased(t *testing.T) {
	big := "\x1b]52;c;" + string(make([]byte, maxPendingInput))
	got, tk := tokenize(t, false, big)
	require.Len(t, got, 1)
	assert.Equal(t, tokKey, got[0].k)
	assert.True(t, tk.atBoundary(false))
}

func TestInputTok_TrackFastPath(t *testing.T) {
	var tk inputTok
	tk.track([]byte("plain typing"))
	assert.True(t, tk.atBoundary(false))
	tk.track([]byte("x\x1b[1"))
	assert.False(t, tk.atBoundary(true), "tracking sees an unfinished CSI")
	tk.track([]byte("2;40R"))
	assert.True(t, tk.atBoundary(false))
	tk.track([]byte("\x1b[200~"))
	tk.track([]byte("paste with no escape in this read"))
	assert.False(t, tk.atBoundary(true), "a paste in progress stays in progress across a read with no escape")
	tk.track([]byte("\x1b[201~"))
	assert.True(t, tk.atBoundary(false))
}
