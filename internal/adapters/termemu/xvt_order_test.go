package termemu

import (
	"io"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
)

// laggingReader holds each chunk the reply drain reads until the next one
// has been written: the drain is then always one write behind whatever was
// sent last, which is the interleaving an ordering bug needs, forced.
type laggingReader struct {
	r    io.Reader
	held []byte
}

func (l *laggingReader) fill() ([]byte, error) {
	buf := make([]byte, 4096)
	n, err := l.r.Read(buf)
	if n == 0 && err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (l *laggingReader) Read(p []byte) (int, error) {
	if l.held == nil {
		held, err := l.fill()
		if err != nil {
			return 0, err
		}
		l.held = held
	}
	next, _ := l.fill() // nil at the pipe's close: the next Read reports it
	n := copy(p, l.held)
	l.held = next
	return n, nil
}

// lagDrain makes every pane this test builds drain through a laggingReader.
func lagDrain(t *testing.T) {
	t.Helper()
	prev := drainSource
	drainSource = func(r io.Reader) io.Reader { return &laggingReader{r: r} }
	t.Cleanup(func() { drainSource = prev })
}

// Every input the pane sends reaches the child in the order it was sent,
// however far the reply drain lags: a key sent after a mouse report or a
// paste never overtakes it.
func TestInput_ReachesTheChildInSendOrder(t *testing.T) {
	lagDrain(t)
	p, sink := newPane(t, 80, 24)
	_, _ = p.Write([]byte("\x1b[?1000h\x1b[?1006h\x1b[?2004h"))
	p.SendMouse(uv.MouseClickEvent{X: 4, Y: 2, Button: uv.MouseLeft})
	p.SendKey(KeyPress{Code: 'a', Text: "a"})
	p.Paste("p")
	p.SendKey(KeyPress{Code: 'b', Text: "b"})
	// The DECRQM reply comes last; the DA query behind it pushes it out of
	// the lagging drain.
	_, _ = p.Write([]byte("\x1b[?2026$p\x1b[c"))
	assert.Equal(t, "\x1b[<0;5;3Ma\x1b[200~p\x1b[201~b\x1b[?2026;0$y", sink.waitFor(t, "$y"))
}
