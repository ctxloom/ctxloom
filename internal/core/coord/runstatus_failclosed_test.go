package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestCaptureRunFailure_ReadsTerminalStatusAsAnAllowList pins the second half
// of a finding: captureRunFailure tested `!= RUN_STATUS_FAILED` and returned,
// so a run that ended UNSPECIFIED (the enum's zero value — what an engine that
// never set a status produces) or TIMED_OUT/BUDGET_EXCEEDED had its dying
// words silently dropped and the parent got no reason at all. Success is the
// allow-list; CANCELLED stays excluded because a deliberate stop is not a
// failure to explain.
func TestCaptureRunFailure_ReadsTerminalStatusAsAnAllowList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  agentcoordpb.Result_RunStatus
		capture bool
	}{
		{"succeeded", agentcoordpb.Result_RUN_STATUS_SUCCEEDED, false},
		{"cancelled", agentcoordpb.Result_RUN_STATUS_CANCELLED, false},
		{"failed", agentcoordpb.Result_RUN_STATUS_FAILED, true},
		{"unspecified (the zero value)", agentcoordpb.Result_RUN_STATUS_UNSPECIFIED, true},
		{"timed_out", agentcoordpb.Result_RUN_STATUS_TIMED_OUT, true},
		{"budget_exceeded", agentcoordpb.Result_RUN_STATUS_BUDGET_EXCEEDED, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Coordinator{byHarp: map[string]*childRt{"kid": {}}}
			c.captureRunFailure("kid", &agentcoordpb.AgentEvent{
				Payload: &agentcoordpb.AgentEvent_RunCompleted{
					RunCompleted: &agentcoordpb.RunCompleted{
						Result: &agentcoordpb.Result{Status: tc.status, Text: "the adapter died"},
					},
				},
			})
			got := c.byHarp["kid"].runFailure
			if tc.capture {
				assert.Equal(t, "the adapter died", got, "terminal status %v must have its reason recorded", tc.status)
				return
			}
			assert.Empty(t, got, "terminal status %v is not a failure to explain", tc.status)
		})
	}
}
