package termui

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
	"github.com/stretchr/testify/require"
)

// tokenPool is the input vocabulary of the conservation property: keys (a
// rune, a control, cursor and function keys, alt+key), terminal replies,
// mouse reports, and a bracketed paste with an escape sequence and a CR in
// it. A lone ESC is left out: followed by anything it IS a different key.
var tokenPool = []struct {
	s string
	k tokKind
}{
	{"a", tokKey}, {"é", tokKey}, {"\r", tokKey}, {"y", tokKey}, {"1", tokKey},
	{"\x1b[A", tokKey}, {"\x1bOP", tokKey}, {"\x1ba", tokKey}, {"\x1b[1;5C", tokKey},
	{"\x1b[12;40R", tokReport}, {"\x1b[?62;22c", tokReport}, {"\x1b]11;rgb:1/2/3\x1b\\", tokReport},
	{"\x1b[I", tokReport}, {"\x1b[?2026;2$y", tokReport}, {"\x1bP>|xterm(390)\x1b\\", tokReport},
	{"\x1b[<0;1;2M", tokMouse}, {"\x1b[M !!", tokMouse},
	{"\x1b[200~ab\r\x1b[31mc\x1b[201~", tokPasteBegin},
}

type propToken struct {
	start, end int
	k          tokKind
	decideAt   int // offset whose arrival decides the token's side
}

// TestInterceptor_ConservesEveryTokenAcrossASummon is T1: for any stream and
// any read boundaries, a summon attempted at any read lands only at a token
// boundary outside a paste, and afterwards
//
//	engine == input before the switch ++ every later terminal reply,
//	modal  == every later key or paste decided after arming ended,
//	discarded == the count of later keys/pastes decided while arming,
//
// with mouse reports going nowhere — so every token lands whole on exactly
// one side and no modal-bound key precedes the end of arming.
//
// Cuts one or two bytes into an ESC-led token are not generated: a stream
// paused there is indistinguishable from the Esc/Alt key the quiet gate takes
// it for (inputTok.atBoundary), and a terminal never pauses mid-sequence.
func TestInterceptor_ConservesEveryTokenAcrossASummon(t *testing.T) {
	rng := rand.New(rand.NewPCG(20260930, 5))
	for iter := range 4000 {
		stream, toks := genStream(rng)
		cuts := genCuts(rng, stream, toks)
		j := rng.IntN(len(cuts))       // summon attempted before read j, j+1, …
		arm := j + rng.IntN(len(cuts)) // arming ends after read arm
		checkConservation(t, iter, stream, toks, cuts, j, arm)
	}
}

func genStream(rng *rand.Rand) (string, []propToken) {
	var b strings.Builder
	var toks []propToken
	for range 1 + rng.IntN(12) {
		p := tokenPool[rng.IntN(len(tokenPool))]
		start := b.Len()
		b.WriteString(p.s)
		decide := b.Len()
		if p.k == tokPasteBegin {
			decide = start + len("\x1b[200~")
		}
		toks = append(toks, propToken{start: start, end: b.Len(), k: p.k, decideAt: decide})
	}
	return b.String(), toks
}

// genCuts returns read boundaries (ascending, ending at len(stream)).
func genCuts(rng *rand.Rand, stream string, toks []propToken) []int {
	var cuts []int
	for off := 1; off < len(stream); off++ {
		if rng.IntN(3) == 0 && !ambiguousCut(stream, toks, off) {
			cuts = append(cuts, off)
		}
	}
	return append(cuts, len(stream))
}

func ambiguousCut(stream string, toks []propToken, off int) bool {
	for _, tk := range toks {
		if stream[tk.start] == 0x1b && off-tk.start >= 1 && off-tk.start <= 2 && off < tk.end {
			return true
		}
	}
	return false
}

func checkConservation(t *testing.T, iter int, stream string, toks []propToken, cuts []int, j, arm int) {
	clk := fakeclock.New()
	ic := newInterceptor(nil, testPrefix, InterceptorCallbacks{}, clk.Now)
	var engine, modal bytes.Buffer
	switchAt, switched, armRead := -1, false, -1
	prev := 0
	for i, c := range cuts {
		if i >= j && !switched {
			clk.Advance(time.Hour) // a quiet pause before this read
			if _, ok := ic.engageExternal(&modal, time.Second, time.Second, 1); ok {
				switched, switchAt, armRead = true, prev, max(arm, i)
				_, ok := ic.armFrame(1)
				require.True(t, ok)
			}
		}
		out, ui, act := ic.scan([]byte(stream[prev:c]))
		engine.Write(out)
		ic.dispatch(ui, act)
		if switched && i == armRead {
			clk.Advance(time.Second)
			armed, _, _, ok := ic.tryArm(1)
			require.True(t, ok && armed, "iter %d: arming ends once its window has passed", iter)
		}
		prev = c
	}
	want := expectConservation(stream, toks, cuts, switchAt, armRead)
	ctx := func() string { return fmt.Sprintf("iter %d stream %q cuts %v switch %d", iter, stream, cuts, switchAt) }
	require.Equal(t, firstBoundaryRead(toks, cuts, j), switchAt,
		"%s: focus moves at the first attempt made at a token boundary, and at no other", ctx())
	require.Equal(t, want.engine, engine.String(), "%s: engine side", ctx())
	require.Equal(t, want.modal, modal.String(), "%s: modal side", ctx())
	require.Equal(t, want.discarded, ic.modal.discarded*boolInt(switched), "%s: discarded", ctx())
}

type sides struct {
	engine, modal string
	discarded     int
}

// expectConservation computes where each token must land. A read decides a
// token when it carries the token's deciding byte; arming ends after read
// armRead.
func expectConservation(stream string, toks []propToken, cuts []int, switchAt, armRead int) sides {
	if switchAt < 0 {
		return sides{engine: stream}
	}
	s := sides{engine: stream[:switchAt]}
	var modal strings.Builder
	var engine strings.Builder
	engine.WriteString(s.engine)
	armedFrom := cuts[min(armRead, len(cuts)-1)]
	for _, tk := range toks {
		if tk.start < switchAt {
			continue
		}
		text := stream[tk.start:tk.end]
		switch {
		case tk.k == tokReport:
			engine.WriteString(text)
		case tk.k == tokMouse:
		case readOf(cuts, tk.decideAt) <= readOf(cuts, armedFrom):
			s.discarded++
		default:
			modal.WriteString(text)
		}
	}
	s.engine, s.modal = engine.String(), modal.String()
	return s
}

// readOf is the index of the read that carries byte offset off-1 (the read
// in which a token ending at off completes).
func readOf(cuts []int, off int) int {
	i, _ := slices.BinarySearch(cuts, off)
	return i
}

// firstBoundaryRead is the offset of the first read from index j on that
// starts at a token boundary (-1: none).
func firstBoundaryRead(toks []propToken, cuts []int, j int) int {
	for i := j; i < len(cuts); i++ {
		start := 0
		if i > 0 {
			start = cuts[i-1]
		}
		if isTokenBoundary(toks, start) {
			return start
		}
	}
	return -1
}

func isTokenBoundary(toks []propToken, off int) bool {
	if off == 0 {
		return true
	}
	for _, tk := range toks {
		if tk.end == off {
			return true
		}
	}
	return false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
