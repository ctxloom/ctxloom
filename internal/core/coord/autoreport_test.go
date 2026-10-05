package coord

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The automatic report's structure is data a parent branches on: the
// marker always, the refused calls when there were any, and the plan
// awaiting approval when the turn ended holding one.
func TestAutoReportStructured(t *testing.T) {
	bare := AutoReportStructured(AutoReport{})
	assert.JSONEq(t, `{"auto_report":true}`, string(bare), "nothing to say beyond the marker")
	assert.True(t, IsAutoReport(bare))

	full := AutoReportStructured(AutoReport{
		Blocked: []BlockedCall{{Tool: "Bash", Reason: "denied", Decider: "rule"}},
		PlanApproval: &PlanApproval{
			Artifact: "plan/plan-add-hello",
			Postures: []engine.PostureTransition{
				{Posture: "default", Label: "default"},
				{Posture: "acceptEdits", Label: "accept edits", Default: true},
			},
		},
	})
	assert.True(t, IsAutoReport(full))
	var got struct {
		Blocked      []BlockedCall `json:"blocked"`
		PlanApproval struct {
			Artifact string `json:"artifact"`
			Postures []struct {
				Posture string `json:"posture"`
				Label   string `json:"label"`
				Default bool   `json:"default"`
			} `json:"postures"`
		} `json:"plan_approval"`
	}
	require.NoError(t, json.Unmarshal(full, &got))
	assert.Equal(t, []BlockedCall{{Tool: "Bash", Reason: "denied", Decider: "rule"}}, got.Blocked)
	assert.Equal(t, "plan/plan-add-hello", got.PlanApproval.Artifact)
	require.Len(t, got.PlanApproval.Postures, 2)
	assert.Equal(t, "acceptEdits", got.PlanApproval.Postures[1].Posture)
	assert.True(t, got.PlanApproval.Postures[1].Default, "the default is explicit, never an offer's position")
}
