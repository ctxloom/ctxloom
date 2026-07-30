package coord

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// U024-F21: a malformed ctxloom/* custom event was indistinguishable from an
// absent one. `ctxloom/mail_consumed` with no usable message_ids simply
// returned, and `ctxloom/harness_session` with no session_id fell through
// recordHarnessSession's empty-id guard — so a runner that lost its consumption
// cursor, or a run that lost its ONLY resume handle, produced no signal at all.
//
// Each case asserts the PAYLOAD too (the cursor did not advance / no fact was
// recorded), never just the log line: the log is how an operator learns, the
// payload is what actually happened.
func TestHandleCustomEvent_MalformedEventsAreReported(t *testing.T) {
	custom := func(t *testing.T, name string, value map[string]any) (string, *Coordinator, string) {
		t.Helper()
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinator(t, sp, nil)
		role := "child-malformed"
		ch := &runChan{
			role:      role,
			id:        Identity{Harp: role, RunID: "run-malformed"},
			send:      make(chan *agentcoordpb.CoordinatorFrame, 4),
			completed: make(chan struct{}),
		}
		var val *structpb.Struct
		if value != nil {
			var err error
			val, err = structpb.NewStruct(value)
			require.NoError(t, err)
		}
		var buf bytes.Buffer
		restore := clidiag.SetSink(&buf)
		defer restore()
		c.handleCustomEvent(ch, &agentcoordpb.CustomEvent{Name: name, Value: val})
		return buf.String(), c, role
	}

	t.Run("mail_consumed with an empty message_ids list is reported and the cursor does not advance", func(t *testing.T) {
		sp := newFakeSpawner(nil, nil)
		c := newTestCoordinator(t, sp, nil)
		role := "child-cursor"
		_, _, err := c.queueMail("owner", role, "task", "do the thing")
		require.NoError(t, err)
		require.Equal(t, 1, c.pendingCount(role), "precondition: the message is deliverable")

		ch := &runChan{role: role, id: Identity{Harp: role, RunID: "run-cursor"}, send: make(chan *agentcoordpb.CoordinatorFrame, 4), completed: make(chan struct{})}
		val, err := structpb.NewStruct(map[string]any{"message_ids": []any{}})
		require.NoError(t, err)

		var buf bytes.Buffer
		restore := clidiag.SetSink(&buf)
		c.handleCustomEvent(ch, &agentcoordpb.CustomEvent{Name: CustomMailConsumed, Value: val})
		restore()

		assert.Equal(t, 1, c.pendingCount(role),
			"nothing usable was consumed, so the message must still be deliverable")
		assert.Contains(t, buf.String(), "no usable message_ids",
			"a lost consumption claim must be reported, not silently dropped")
	})

	t.Run("mail_consumed whose entries are not strings reports the drops", func(t *testing.T) {
		out, _, _ := custom(t, CustomMailConsumed, map[string]any{"message_ids": []any{"", 5.0}})
		assert.Contains(t, out, "unusable message_ids")
		assert.Contains(t, out, "no usable message_ids")
	})

	t.Run("mail_consumed whose message_ids is not a list at all is reported", func(t *testing.T) {
		out, _, _ := custom(t, CustomMailConsumed, map[string]any{"message_ids": "m-1"})
		assert.Contains(t, out, "unusable message_ids",
			"a scalar where a list belongs must not read as an absent field")
	})

	t.Run("harness_session with no session_id is reported as a lost resume handle", func(t *testing.T) {
		out, _, _ := custom(t, CustomHarnessSession, map[string]any{"resumable": true})
		assert.Contains(t, out, "no session_id")
		assert.Contains(t, out, "resume handle")
	})

	t.Run("harness_session with no value at all is reported too", func(t *testing.T) {
		out, _, _ := custom(t, CustomHarnessSession, nil)
		assert.Contains(t, out, "no session_id")
	})

	t.Run("a well-formed harness_session is silent", func(t *testing.T) {
		out, _, _ := custom(t, CustomHarnessSession, map[string]any{"session_id": "sess-1", "resumable": true})
		assert.Empty(t, out, "the ordinary path must not warn")
	})
}

// stringList's drop count is what lets a caller tell "the field said nothing"
// apart from "the field said something unreadable". An ABSENT key is not a
// fault.
func TestStringList_ReportsUnusableEntries(t *testing.T) {
	mk := func(t *testing.T, m map[string]any) *structpb.Struct {
		t.Helper()
		s, err := structpb.NewStruct(m)
		require.NoError(t, err)
		return s
	}

	out, dropped := stringList(nil, "message_ids")
	assert.Empty(t, out)
	assert.Zero(t, dropped, "a nil struct is not a malformed field")

	out, dropped = stringList(mk(t, map[string]any{"other": "x"}), "message_ids")
	assert.Empty(t, out)
	assert.Zero(t, dropped, "an absent key is not a malformed field")

	out, dropped = stringList(mk(t, map[string]any{"message_ids": []any{"a", "b"}}), "message_ids")
	assert.Equal(t, []string{"a", "b"}, out)
	assert.Zero(t, dropped)

	out, dropped = stringList(mk(t, map[string]any{"message_ids": []any{"a", "", 3.0, true}}), "message_ids")
	assert.Equal(t, []string{"a"}, out)
	assert.Equal(t, 3, dropped)

	out, dropped = stringList(mk(t, map[string]any{"message_ids": "a"}), "message_ids")
	assert.Empty(t, out)
	assert.Equal(t, 1, dropped, "a non-list value counts as one unusable entry")
}
