package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// unwritableHarpDir makes harp's persist/ dir uncreatable by chmod'ing its
// parent to 0o000 — a REAL EACCES from the real filesystem, not a fake that
// returns a canned error. Returns the transcript path the recorder will
// target.
func unwritableHarpDir(t *testing.T, harp string) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0o000 does not deny root, so this EACCES path cannot be provoked")
	}
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	harpDir := filepath.Dir(filepath.Dir(path)) // .../<harp>, parent of persist/
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	require.NoError(t, os.Chmod(harpDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(harpDir, 0o755) })
	return path
}

// TestRecorder_WriteFailure_IsObservable pins U144-F04. A transcript write
// that fails — full disk, EACCES, a path that cannot be created — used to
// produce a completely green chat, zero captured bytes, and NOT ONE
// diagnostic anywhere: Tee, TeeAndClose, RecordUserText and
// CoordinatedRecorder all discard Record's error with `_ =`, and no counter
// or log stood behind them. That is this project's signature failure mode
// (exit 0, success message, zero bytes delivered).
//
// The swallow at those call sites is deliberate and stays — capture must
// never perturb the live chat it shadows. The fix is that the RECORDER
// itself, which is the only party that knows a write failed, says so.
func TestRecorder_WriteFailure_IsObservable(t *testing.T) {
	t.Run("Record failure warns, and Close reports the total", func(t *testing.T) {
		testsupport.Isolate(t)
		const harp = "eacces-harp"
		path := unwritableHarpDir(t, harp)

		var sink bytes.Buffer
		restore := clidiag.SetSink(&sink)
		defer restore()

		rec, err := NewRecorder(harp, "codex")
		require.NoError(t, err)

		ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hello"}}
		require.Error(t, rec.Record(ev), "the write cannot succeed under EACCES")
		require.Error(t, rec.Record(ev))

		assert.Contains(t, sink.String(), "eacces-harp",
			"a failed transcript write must name the harp it lost")
		assert.Contains(t, sink.String(), path,
			"...and the path it could not write")
		assert.Equal(t, 1, strings.Count(sink.String(), "transcript capture failed"),
			"warn ONCE per recorder, not once per event: a failing disk must not flood the chat with N warnings")

		require.NoError(t, rec.Close())
		assert.Contains(t, sink.String(), "2 event(s)",
			"Close must report the TOTAL lost, which is the number only the recorder knows")

		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr) || statErr != nil,
			"sanity: nothing was actually captured — this is the zero-bytes case, not a false alarm")
	})

	t.Run("the live tee path is observable end to end", func(t *testing.T) {
		testsupport.Isolate(t)
		const harp = "eacces-tee-harp"
		unwritableHarpDir(t, harp)

		var sink bytes.Buffer
		restore := clidiag.SetSink(&sink)
		defer restore()

		rec, err := NewRecorder(harp, "codex")
		require.NoError(t, err)

		in := make(chan agent.ChatEvent, 1)
		in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi"}}
		close(in)

		var got int
		for range TeeAndClose(rec, in) {
			got++
		}
		assert.Equal(t, 1, got, "the chat itself must be unaffected: every event still forwarded")
		assert.Contains(t, sink.String(), "transcript capture failed",
			"a chat whose entire transcript was lost must not look identical to one that was captured")
	})
}
