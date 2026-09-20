package coord

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// A malformed ctxloom/* custom event was indistinguishable from an
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
			role:        role,
			id:          Identity{Harp: role, RunID: "run-malformed"},
			bidiSession: newBidiSession[*agentcoordpb.CoordinatorFrame, *agentcoordpb.CoordinatorFrame, *agentcoordpb.AgentFrame](func() {}, 4),
			completed:   make(chan struct{}),
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
