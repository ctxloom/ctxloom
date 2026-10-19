package termui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
	"github.com/ctxloom/ctxloom/internal/testsupport/termoracle"
)

// The oracle's tables run here against the layer they were taken from, so a
// table that stops describing termui fails where it was written, not only in
// the replacement that reads it.

func TestOracle_PrefixKeyTable(t *testing.T) {
	b, err := ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	k := newPrefixKey(b)
	want := map[termoracle.Hit]keyHit{termoracle.Miss: keyMiss, termoracle.Press: keyPress, termoracle.Release: keyRelease}
	for _, c := range append(termoracle.CtrlBracket, termoracle.CtrlBracketLenient...) {
		assert.Equal(t, want[c.Want], k.hit([]byte(c.Bytes)), "%s: %q", c.Name, c.Bytes)
	}
}

// TestOracle_TokenCorpusTokenizesWhole pins that the tokenizer splits the
// corpus, run together in one read, back into exactly its tokens, each with
// its kind; a paste is its begin marker, its body and its end marker.
func TestOracle_TokenCorpusTokenizesWhole(t *testing.T) {
	kinds := map[termoracle.TokenKind]tokKind{termoracle.Key: tokKey, termoracle.Report: tokReport, termoracle.Mouse: tokMouse}
	var stream strings.Builder
	var want []string
	for _, tk := range termoracle.Tokens {
		stream.WriteString(tk.Bytes)
		if tk.Kind == termoracle.Paste {
			body := strings.TrimSuffix(strings.TrimPrefix(tk.Bytes, "\x1b[200~"), "\x1b[201~")
			want = append(want, kindTag(tokPasteBegin, "\x1b[200~"), kindTag(tokPasteData, body), kindTag(tokPasteEnd, "\x1b[201~"))
			continue
		}
		want = append(want, kindTag(kinds[tk.Kind], tk.Bytes))
	}
	var tok inputTok
	var got []string
	tok.feed([]byte(stream.String()), true, func(k tokKind, b []byte) { got = append(got, kindTag(k, string(b))) })
	assert.Equal(t, want, got)
}

func kindTag(k tokKind, s string) string {
	return map[tokKind]string{tokKey: "key", tokReport: "report", tokMouse: "mouse", tokPasteBegin: "paste[", tokPasteData: "paste", tokPasteEnd: "paste]"}[k] + " " + s
}

// TestOracle_TokenCorpusPassesThroughUnengaged pins the passthrough: with no
// overlay up, every token reaches the engine byte for byte.
func TestOracle_TokenCorpusPassesThroughUnengaged(t *testing.T) {
	for _, tk := range termoracle.Tokens {
		ic := newInterceptor(nil, testPrefix, InterceptorCallbacks{}, fakeclock.New().Now)
		out, ui, _ := ic.scan([]byte(tk.Bytes))
		assert.Equal(t, tk.Bytes, string(out), "%s reaches the engine whole", tk.Name)
		assert.Empty(t, ui, "%s reaches no overlay", tk.Name)
	}
}

// TestOracle_SummonContract runs the approvals focus-lock contract against
// the controller.
func TestOracle_SummonContract(t *testing.T) {
	termoracle.RunSummonContract(t, func(t *testing.T) termoracle.Summoner {
		h := newSummonHarness(t, nil)
		// The modal's first frame is what starts its inert window.
		h.overlay.frame = "FRAME"
		return &termuiSummoner{h: h, t: t}
	})
}

// termuiSummoner is the controller as the contract drives it.
type termuiSummoner struct {
	h     *summonHarness
	t     *testing.T
	shown bool
	armed bool
	disc  int
}

func (s *termuiSummoner) Type(t *testing.T, keys string) {
	t.Helper()
	before := s.h.overlay.ui.String()
	s.h.typeKeys(t, keys)
	if s.shown && s.armed {
		// Keys reach an armed overlay through its pipe's copier.
		s.h.overlay.ui.waitUntil(t, "the keys at the overlay", func(got string) bool { return got == before+keys })
	}
}

func (s *termuiSummoner) Engine() string { return s.h.engine.String() }

func (s *termuiSummoner) Summon(ctx context.Context) <-chan error { return s.h.summon(ctx) }

func (s *termuiSummoner) Parked(t *testing.T) {
	t.Helper()
	s.h.waitTimers(t, 1)
}

func (s *termuiSummoner) Advance(t *testing.T, d time.Duration) {
	t.Helper()
	s.h.clk.Advance(d)
	select {
	case n := <-s.h.overlay.armed:
		s.armed, s.disc = true, n
	default:
	}
}

// Outcome also waits, for a shown summon, until the modal's first frame has
// started its inert window: until then a key would race the arming timer.
func (s *termuiSummoner) Outcome(err error) termoracle.Outcome {
	switch {
	case err == nil:
		await(s.t, "the modal starting", s.h.overlay.started)
		s.shown = true
		s.h.waitTimers(s.t, 1)
		return termoracle.Shown
	case errors.Is(err, ErrOverlayEngaged):
		return termoracle.Engaged
	case errors.Is(err, context.Canceled):
		return termoracle.Cancelled
	}
	return termoracle.Unavailable
}

// Shown also reports a modal that started without its Summon having been
// received: a withdrawn summon that took the screen anyway.
func (s *termuiSummoner) Shown() bool {
	select {
	case geo := <-s.h.overlay.started:
		s.h.overlay.started <- geo
		return true
	default:
		return s.shown
	}
}

func (s *termuiSummoner) Armed() (bool, int) {
	if s.armed {
		return true, s.disc
	}
	return false, s.h.discarded()
}

func (s *termuiSummoner) Approval() string {
	if !s.armed {
		return ""
	}
	return s.h.overlay.ui.String()
}

func (s *termuiSummoner) Engage(t *testing.T) {
	t.Helper()
	s.h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	await(t, "the overlay engaging", s.h.overlay.started)
}

func (s *termuiSummoner) Notices() []string {
	var out []string
	for {
		select {
		case n := <-s.h.overlay.notices:
			out = append(out, n.Text)
		default:
			return out
		}
	}
}

func (s *termuiSummoner) Close() { s.h.c.Close() }
