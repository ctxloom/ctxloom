package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestOneShot_TurnCapture_WritesTwoEntryTranscript: a one-shot turn returns
// prose on stdout with no event stream, so the structured capture never
// fires for it; the turn records the two-entry transcript on the session's
// OWN harp — every one-shot owns one.
func TestOneShot_TurnCapture_WritesTwoEntryTranscript(t *testing.T) {
	testsupport.Isolate(t)
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubClient{out: "  the assistant's captured stdout  \n"}

	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	out, err := o.Turn(context.Background(), "the user's request prompt")
	require.NoError(t, err)
	assert.Equal(t, "the assistant's captured stdout", out)

	harp := o.Launch.Identity.Harp
	require.NotEmpty(t, harp)
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	sess, err := transcript.ParseTranscriptFile(path, harp)
	require.NoError(t, err)
	require.Len(t, sess.Entries, 2)

	assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
	assert.Equal(t, "the user's request prompt", sess.Entries[0].Content)

	assert.Equal(t, agent.EntryTypeAssistant, sess.Entries[1].Type)
	assert.Equal(t, "the assistant's captured stdout", sess.Entries[1].Content)
}
