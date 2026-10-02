package claude

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// eventLog is an in-memory transcript.Recorder: the events, in order.
type eventLog struct{ evs []agent.ChatEvent }

func (l *eventLog) Record(ev agent.ChatEvent) error { l.evs = append(l.evs, ev); return nil }
func (l *eventLog) Close() error                    { return nil }

// countingSource counts the bytes a conversion actually pulls from its source.
type countingSource struct {
	io.ReadSeeker
	n int64
}

func (s *countingSource) Read(p []byte) (int, error) {
	n, err := s.ReadSeeker.Read(p)
	s.n += int64(n)
	return n, err
}

// captured is one convertFrom run: every event recorded, how many of them
// preceded the checkpoint, the checkpoint (nil when none was offered), and the
// source bytes read.
type captured struct {
	evs  []agent.ChatEvent
	atCP int
	cp   *vendorreader.Checkpoint
	read int64
	acct importAccounting
}

func convertCapture(t *testing.T, src []byte, from vendorreader.Checkpoint) (captured, error) {
	t.Helper()
	var c captured
	log := &eventLog{}
	r := &countingSource{ReadSeeker: bytes.NewReader(src)}
	acct, err := convertFrom(context.Background(), log, r, "fixture", from, func(cp vendorreader.Checkpoint) error {
		require.Nil(t, c.cp, "onCheckpoint is called at most once")
		c.cp, c.atCP = &cp, len(log.evs)
		return nil
	})
	c.evs, c.read, c.acct = log.evs, r.n, acct
	return c, err
}

// fixtureLines is the claude fixture split into its raw lines, newline kept.
func fixtureLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, "transcript-fixture.jsonl"))
	require.NoError(t, err)
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		out = append(out, l+"\n")
	}
	return out
}

func joined(lines []string) []byte { return []byte(strings.Join(lines, "")) }

// headerSettledAt is the first fixture line count at which the session id,
// model and permission mode have all appeared — before it the conversion's
// one-time Session record could still change, so nothing may checkpoint.
const headerSettledAt = 6

// TestConvertFrom_ResumeReproducesTheFullConversion is the property the whole
// watermark rests on: converting a prefix, then resuming over the grown file
// from the prefix's checkpoint, records EXACTLY what one full conversion of
// the grown file records — at every split point. The prefix's provisional
// tail (its end-of-file Complete) is dropped at the checkpoint and re-derived
// by the resume, which is how an open turn spanning the split stays one turn.
func TestConvertFrom_ResumeReproducesTheFullConversion(t *testing.T) {
	lines := fixtureLines(t)
	full, err := convertCapture(t, joined(lines), vendorreader.Checkpoint{})
	require.NoError(t, err)
	require.Equal(t, len(lines), full.acct.lines, "the floor's total is every line read")

	for k := headerSettledAt; k <= len(lines); k++ {
		pre, err := convertCapture(t, joined(lines[:k]), vendorreader.Checkpoint{})
		require.NoError(t, err)
		require.NotNil(t, pre.cp, "a settled prefix of %d lines must offer a checkpoint", k)

		resumed, err := convertCapture(t, joined(lines), *pre.cp)
		require.NoError(t, err)

		got := append(append([]agent.ChatEvent{}, pre.evs[:pre.atCP]...), resumed.evs...)
		assert.Equal(t, full.evs, got, "split after line %d", k)
		// The end-of-file floor judges the WHOLE file, so a resume must carry
		// the counts the prefix accumulated (drop tallies are per-read and
		// deliberately not carried).
		floor := func(a importAccounting) [4]int { return [4]int{a.lines, a.conversational, a.malformed, a.entries} }
		assert.Equal(t, floor(full.acct), floor(resumed.acct), "accounting after a split at line %d", k)
	}
}

// TestConvertFrom_PairSpanningTheCheckpointStaysWhole names the case the
// equality above must cover, so the fixture cannot drift into one where no
// split separates a tool_use from its tool_result: after line 8 the call is
// on the checkpointed side and its result is not.
func TestConvertFrom_PairSpanningTheCheckpointStaysWhole(t *testing.T) {
	lines := fixtureLines(t)
	pre, err := convertCapture(t, joined(lines[:8]), vendorreader.Checkpoint{})
	require.NoError(t, err)
	require.NotNil(t, pre.cp)
	resumed, err := convertCapture(t, joined(lines), *pre.cp)
	require.NoError(t, err)

	var callID string
	for _, ev := range pre.evs[:pre.atCP] {
		if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeToolUse {
			callID = ev.Entry.ToolCallID
		}
	}
	require.NotEmpty(t, callID, "the checkpointed side must hold a tool_use")
	var answered bool
	for _, ev := range resumed.evs {
		if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeToolResult && ev.Entry.ToolCallID == callID {
			answered = true
		}
	}
	assert.True(t, answered, "the resumed side must carry that tool_use's result")
}

// TestConvertFrom_ResumeReadsOnlyFromTheCheckpointedLine is the cost claim:
// growth is converted without re-reading the prefix. The one line before the
// offset is re-read on purpose (Checkpoint.Seek's identity check); nothing
// earlier is.
func TestConvertFrom_ResumeReadsOnlyFromTheCheckpointedLine(t *testing.T) {
	lines := fixtureLines(t)
	pre, err := convertCapture(t, joined(lines[:headerSettledAt+1]), vendorreader.Checkpoint{})
	require.NoError(t, err)
	require.NotNil(t, pre.cp)

	src := joined(lines)
	resumed, err := convertCapture(t, src, *pre.cp)
	require.NoError(t, err)
	assert.Equal(t, int64(len(src))-pre.cp.Start, resumed.read)
	assert.Greater(t, pre.cp.Start, int64(0), "the prefix a resume skips must be non-empty, or the count above proves nothing")
}

// TestConvertFrom_NoCheckpointBeforeTheHeaderSettles: the Session record is
// written once, at the top, from the first occurrence of each field. A prefix
// that has not seen all of them yet would freeze an incomplete header into
// every resumed transcript, so it offers no checkpoint and the next refresh
// converts in full.
func TestConvertFrom_NoCheckpointBeforeTheHeaderSettles(t *testing.T) {
	lines := fixtureLines(t)
	pre, err := convertCapture(t, joined(lines[:headerSettledAt-1]), vendorreader.Checkpoint{})
	require.NoError(t, err)
	assert.Nil(t, pre.cp)
}

// TestConvertFrom_NeverCheckpointsPastAnUnterminatedLine: a last line with no
// newline may still be mid-write. Its records are provisional, recorded after
// the checkpoint, so the next resume re-reads it whole.
func TestConvertFrom_NeverCheckpointsPastAnUnterminatedLine(t *testing.T) {
	lines := fixtureLines(t)
	k := headerSettledAt + 2
	src := append(joined(lines[:k]), []byte(strings.TrimRight(lines[k], "\n"))...)
	pre, err := convertCapture(t, src, vendorreader.Checkpoint{})
	require.NoError(t, err)
	require.NotNil(t, pre.cp)
	assert.EqualValues(t, len(joined(lines[:k])), pre.cp.Offset)
	assert.Less(t, pre.atCP, len(pre.evs), "the unterminated line's records come after the checkpoint")
}

// TestConvertFrom_NothingNewHandsBackTheSameCheckpoint: a refresh over an
// unchanged file must keep its watermark, not lose it.
func TestConvertFrom_NothingNewHandsBackTheSameCheckpoint(t *testing.T) {
	src := joined(fixtureLines(t))
	pre, err := convertCapture(t, src, vendorreader.Checkpoint{})
	require.NoError(t, err)
	require.NotNil(t, pre.cp)
	again, err := convertCapture(t, src, *pre.cp)
	require.NoError(t, err)
	require.NotNil(t, again.cp)
	assert.Equal(t, *pre.cp, *again.cp)
	assert.Zero(t, again.atCP)
}

// TestConvertFrom_RefusesAMismatchedCheckpoint: a source that no longer
// extends the checkpoint, or state this build cannot read, is
// ErrCheckpointMismatch — the caller's cue to convert in full — never a
// resume over the wrong bytes.
func TestConvertFrom_RefusesAMismatchedCheckpoint(t *testing.T) {
	lines := fixtureLines(t)
	pre, err := convertCapture(t, joined(lines[:headerSettledAt+1]), vendorreader.Checkpoint{})
	require.NoError(t, err)
	require.NotNil(t, pre.cp)

	_, err = convertCapture(t, joined(lines[:headerSettledAt-2]), *pre.cp)
	assert.ErrorIs(t, err, vendorreader.ErrCheckpointMismatch, "a truncated source")

	bad := *pre.cp
	bad.State = []byte(`"not an object"`)
	_, err = convertCapture(t, joined(lines), bad)
	assert.ErrorIs(t, err, vendorreader.ErrCheckpointMismatch, "unreadable adapter state")
}

// TestHeaderSettled: each field the header can carry must be present before a
// conversion may checkpoint — any one missing could still arrive on a later
// line and change the header a full conversion writes.
func TestHeaderSettled(t *testing.T) {
	full := agent.ChatSessionInfo{SessionID: "s", Model: "m", PermissionMode: "p"}
	assert.True(t, headerSettled(&full))
	assert.False(t, headerSettled(nil))
	for name, clear := range map[string]func(*agent.ChatSessionInfo){
		"no session id":      func(i *agent.ChatSessionInfo) { i.SessionID = "" },
		"no model":           func(i *agent.ChatSessionInfo) { i.Model = "" },
		"no permission mode": func(i *agent.ChatSessionInfo) { i.PermissionMode = "" },
	} {
		info := full
		clear(&info)
		assert.False(t, headerSettled(&info), name)
	}
}
