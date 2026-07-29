package iox

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type failAfter struct {
	buf  bytes.Buffer
	n    int
	fail error
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, f.fail
	}
	f.n--
	return f.buf.Write(p)
}

// WriteRaw must not carry its own copy of Write's body — that puts the
// short-circuit guard on e.err in two places on the same field. The two must be
// observationally identical: same bytes delivered, same first error retained,
// same silence after a prior failure.
func TestErrWriter_WriteRawMatchesWrite(t *testing.T) {
	boom := errors.New("boom")

	run := func(raw bool, allow int) (string, error) {
		sink := &failAfter{n: allow, fail: boom}
		e := NewErrWriter(sink)
		for _, chunk := range [][]byte{[]byte("a"), []byte("b"), []byte("c")} {
			if raw {
				e.WriteRaw(chunk)
			} else {
				_, _ = e.Write(chunk)
			}
		}
		return sink.buf.String(), e.Err()
	}

	for _, allow := range []int{0, 1, 2, 3} {
		rawOut, rawErr := run(true, allow)
		wOut, wErr := run(false, allow)
		assert.Equal(t, wOut, rawOut, "allow=%d: WriteRaw must deliver the same bytes as Write", allow)
		assert.Equal(t, wErr, rawErr, "allow=%d: WriteRaw must retain the same first error", allow)
	}
}
