package vendorreader

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkpointAfter reads src and returns the checkpoint a converter would take
// after its line idx.
func checkpointAfter(t *testing.T, src string, idx int) Checkpoint {
	t.Helper()
	lines, err := ReadJSONLLines(strings.NewReader(src), 0)
	require.NoError(t, err)
	return NewCheckpoint(lines[idx], []byte(`{"k":1}`))
}

const checkpointSrc = "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n"

func TestCheckpoint_SeekPositionsAfterTheCheckpointedLine(t *testing.T) {
	cp := checkpointAfter(t, checkpointSrc, 1)
	assert.EqualValues(t, 16, cp.Offset)

	r := bytes.NewReader([]byte(checkpointSrc + "{\"d\":4}\n"))
	require.NoError(t, cp.Seek(r))
	rest, err := ReadJSONLLines(r, cp.Offset)
	require.NoError(t, err)
	require.Len(t, rest, 2)
	assert.Equal(t, `{"c":3}`, string(rest[0].Bytes))
}

func TestCheckpoint_ZeroValueStartsAtTheBeginning(t *testing.T) {
	r := bytes.NewReader([]byte(checkpointSrc))
	require.NoError(t, Checkpoint{}.Seek(r))
	pos, err := r.Seek(0, 1)
	require.NoError(t, err)
	assert.Zero(t, pos)
}

// TestCheckpoint_SeekRefusesASourceThatNoLongerExtendsIt is the vendor half of
// the watermark's self-validation: resuming is only correct when the file is
// the one the checkpoint was taken over, grown by appending. Each case is a
// way that stops being true.
func TestCheckpoint_SeekRefusesASourceThatNoLongerExtendsIt(t *testing.T) {
	cp := checkpointAfter(t, checkpointSrc, 1)
	cases := map[string]string{
		"truncated below the offset":   "{\"a\":1}\n{\"b\"",
		"checkpointed line rewritten":  "{\"a\":1}\n{\"B\":2}\n{\"c\":3}\n",
		"newline no longer at offset":  "{\"a\":1}\n{\"b\":22}\n{\"c\":3}\n",
		"replaced by a different file": strings.Repeat("x", 40),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			err := cp.Seek(bytes.NewReader([]byte(src)))
			assert.ErrorIs(t, err, ErrCheckpointMismatch)
		})
	}
	bad := cp
	bad.Start = bad.Offset
	assert.ErrorIs(t, bad.Seek(bytes.NewReader([]byte(checkpointSrc))), ErrCheckpointMismatch, "an empty checkpointed line is not a checkpoint")
}
