package agent

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A transcript whose every line fails parseLine is indistinguishable from an
// empty one, because ParseSessionFile returns an empty Session and a nil
// error for both and hands the caller no counts. That does not hold: parseLine
// is the CALLER's closure, invoked once per non-empty line, so the caller
// already owns the only counts it needs - which is exactly how
// transcript.ParseTranscriptFile discriminates the two states without
// ParseSessionFile changing shape.
//
// This pins both facts at once: the degrade-to-partial contract (nil error,
// no entries - deliberate, so that one corrupt line never destroys a readable
// session) AND the per-line callback count that makes the contract safe.
// Widening the return signature would break the first to supply the second.
func TestParseSessionFile_UnparseableLinesAreCountableByTheCaller(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCalls int
	}{
		{"empty file", "", 0},
		{"blank lines only", "\n  \n\n", 0},
		{"every line unparseable", "garbage\nmore garbage\nstill garbage\n", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			require.NoError(t, afero.WriteFile(fs, "/t/session.jsonl", []byte(tc.body), 0o600))
			store := SessionStore{FS: fs}

			calls := 0
			sess, err := store.ParseSessionFile("/t/session.jsonl", "sid", func([]byte) []SessionEntry {
				calls++
				return nil
			})

			require.NoError(t, err, "degrade-to-partial: an unreadable line is not a fatal read")
			require.NotNil(t, sess)
			assert.Empty(t, sess.Entries)
			assert.Equal(t, tc.wantCalls, calls,
				"the caller's own closure is the seam that separates 'no content' from 'nothing parsed'")
		})
	}
}
