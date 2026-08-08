package vendorreader

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// fakeRecorder is a minimal transcript.Recorder test double: it captures
// every Record call and can be told to fail the next one, without needing a
// real on-disk fileRecorder (transcript.NewRecorder) for a pure-function
// unit test of RecordFunc/FlushComplete.
type fakeRecorder struct {
	recorded []agent.ChatEvent
	failNext error
}

func (f *fakeRecorder) Record(ev agent.ChatEvent) error {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.recorded = append(f.recorded, ev)
	return nil
}

func (f *fakeRecorder) Close() error { return nil }

func TestRecordFunc_Success(t *testing.T) {
	fr := &fakeRecorder{}
	record := RecordFunc(fr, "codex")

	ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "hi"}}
	require.NoError(t, record(ev))
	require.Len(t, fr.recorded, 1)
	assert.Equal(t, ev, fr.recorded[0])
}

func TestRecordFunc_WrapsErrorWithVendorPrefix(t *testing.T) {
	fr := &fakeRecorder{failNext: errors.New("disk full")}
	record := RecordFunc(fr, "claude")

	err := record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser}})
	require.Error(t, err)
	assert.Equal(t, "claude: record: disk full", err.Error())
}
