package tui

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRawLines_EveryLineEndsCRLF: a terminal in raw mode does not return the
// carriage on LF, so every line written through RawLines must end CR LF — a
// bare LF gains its CR, and a line already ended CR LF is left alone, even
// when its CR and LF arrive in separate writes.
func TestRawLines_EveryLineEndsCRLF(t *testing.T) {
	var out bytes.Buffer
	w := RawLines(&out)
	for _, p := range []string{"one\ntwo\r\nthree\r", "\nfour\n"} {
		n, err := w.Write([]byte(p))
		require.NoError(t, err)
		assert.Equal(t, len(p), n, "a write reports the caller's bytes, not the CRs added to them")
	}
	assert.Equal(t, "one\r\ntwo\r\nthree\r\nfour\r\n", out.String())
}

// TestRawLines_ReportsTheTerminalsWriteError: a write the terminal refuses is
// reported, with none of the caller's bytes counted as written.
func TestRawLines_ReportsTheTerminalsWriteError(t *testing.T) {
	refused := errors.New("terminal gone")
	n, err := RawLines(failingWriter{refused}).Write([]byte("line\n"))
	require.ErrorIs(t, err, refused)
	assert.Zero(t, n)
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
