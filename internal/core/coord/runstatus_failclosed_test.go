package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		status  RunStatus
		capture bool
	}{
		{"succeeded", RunStatusSucceeded, false},
		{"cancelled", RunStatusCancelled, false},
		{"failed", RunStatusFailed, true},
		{"unspecified (the zero value)", RunStatusUnspecified, true},
		{"timed_out", RunStatusTimedOut, true},
		{"budget_exceeded", RunStatusBudgetExceeded, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Coordinator{byHarp: map[string]*childRt{"kid": {}}}
			c.captureRunFailure("kid", Event{Payload: RunCompleted{Result: &Result{Status: tc.status, Text: "the adapter died"}}})
			got := c.byHarp["kid"].runFailure
			if tc.capture {
				assert.Equal(t, "the adapter died", got, "terminal status %v must have its reason recorded", tc.status)
				return
			}
			assert.Empty(t, got, "terminal status %v is not a failure to explain", tc.status)
		})
	}
}
