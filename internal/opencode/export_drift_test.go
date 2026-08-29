//go:build parked_engines

package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetSession_ExportShapeDriftIsLoud pins a real bug: `opencode export`'s
// JSON is read into a struct, and Go silently ignores unknown fields — so ANY
// shape drift (a renamed top-level key, a re-nested messages array, a new part
// vocabulary) unmarshals cleanly into a zero-valued document. The reader then
// returned a Session with `Entries: []` and a nil error, and /recover rendered
// an empty scrollback while reporting success. This is the same class of
// guess-the-private-layout coupling this file's own header records as having
// CONFIRMED BROKEN the kiro reader.
func TestGetSession_ExportShapeDriftIsLoud(t *testing.T) {
	cases := []struct {
		name   string
		export string
		want   string
	}{
		{
			name:   "top-level shape drifted away entirely",
			export: `{"session":{"id":"ses_x"},"turns":[]}`,
			want:   "info.id",
		},
		{
			name:   "messages present but every part type is unrecognised",
			export: `{"info":{"id":"ses_x"},"messages":[{"info":{"role":"user"},"parts":[{"type":"segment","body":"hi"}]}]}`,
			want:   "no entries",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(
				runnerReturning(nil, []byte(tc.export), nil)))
			sess, err := h.GetSession("", "ses_x")
			require.Error(t, err, "a drifted export must fail, not return an empty transcript as a success")
			assert.Nil(t, sess)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The caller asked for a specific session by id; an export carrying a DIFFERENT
// id is the wrong document, and rendering it as that session's scrollback would
// be worse than an empty one.
func TestGetSession_ExportIDMismatchIsLoud(t *testing.T) {
	export := `{"info":{"id":"ses_other"},"messages":[{"info":{"role":"user"},"parts":[{"type":"text","text":"hi"}]}]}`
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(nil, []byte(export), nil)))
	sess, err := h.GetSession("", "ses_x")
	require.Error(t, err)
	assert.Nil(t, sess)
	assert.Contains(t, err.Error(), "ses_other")
}

// A real session with a real message still parses. This is the discriminator
// keeping the checks above from rejecting live exports.
func TestGetSession_WellFormedExportStillParses(t *testing.T) {
	export := `{"info":{"id":"ses_x","time":{"created":1,"updated":2}},"messages":[{"info":{"role":"user"},"parts":[{"type":"text","text":"hi"}]}]}`
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(nil, []byte(export), nil)))
	sess, err := h.GetSession("", "ses_x")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Len(t, sess.Entries, 1)
}
