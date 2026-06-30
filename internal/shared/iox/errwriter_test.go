package iox

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrWriter_WritesThroughAndReportsNoError(t *testing.T) {
	var buf bytes.Buffer
	w := NewErrWriter(&buf)
	w.Printf("a=%d\n", 1)
	w.Println("b")
	w.Print("c")
	w.WriteRaw([]byte("d"))
	require.NoError(t, w.Err())
	assert.Equal(t, "a=1\nb\ncd", buf.String())
}

// failWriter fails every write after the first n succeed.
type failWriter struct {
	ok  int
	err error
}

func (f *failWriter) Write(p []byte) (int, error) {
	if f.ok > 0 {
		f.ok--
		return len(p), nil
	}
	return 0, f.err
}

// TestErrWriter_ShortCircuitsAfterFirstError pins the core invariant: the
// first write error is captured and every later write becomes a no-op, so
// callers can run a long sequence and check Err() exactly once.
func TestErrWriter_ShortCircuitsAfterFirstError(t *testing.T) {
	boom := errors.New("boom")
	fw := &failWriter{ok: 1, err: boom}
	w := NewErrWriter(fw)

	w.Println("first ok")    // succeeds, consumes the one allowed write
	w.Println("second fail") // fails, captures boom
	w.Printf("third %s", "skipped")
	w.Print("fourth skipped")
	w.WriteRaw([]byte("fifth skipped"))

	assert.ErrorIs(t, w.Err(), boom)
	// ok budget was 1; only the first Println should have reached the
	// writer — subsequent calls must short-circuit, leaving ok at 0 and
	// never re-invoking Write past the failure.
	assert.Equal(t, 0, fw.ok)
}
