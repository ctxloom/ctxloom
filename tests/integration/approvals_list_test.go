//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// listedApprovals is the part of `ctxloom session approvals --format json`
// this lane reads.
type listedApprovals struct {
	Coordinators int `json:"coordinators"`
	Approvals    []struct {
		Harp    string   `json:"harp"`
		Summary string   `json:"summary"`
		Lineage []string `json:"lineage"`
	} `json:"approvals"`
}

// runApprovalsList runs the built binary as ANOTHER process: it shares
// nothing with the lane but the HOME its coordinator's endpoint is under.
func runApprovalsList(t *testing.T, format string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), agent.CtxloomCommand(), "--format", format, "session", "approvals").CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(out)
}

// TestPermissionContract_AnotherProcessListsTheParkedAsk: the child's ask
// parks in process A (this test's coordinator); process B, the built
// `ctxloom session approvals`, finds that coordinator by discovery and lists
// the request — and listing it answers nothing: the request is still parked
// for the human afterwards.
func TestPermissionContract_AnotherProcessListsTheParkedAsk(t *testing.T) {
	lane := openContractLane(t, "")
	child := lane.delegate(t)
	p := lane.parked(t)

	var got listedApprovals
	out := runApprovalsList(t, "json")
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.Equal(t, 1, got.Coordinators, "process B reached process A's coordinator")
	require.Len(t, got.Approvals, 1, out)
	assert.Equal(t, child.Harp, got.Approvals[0].Harp)
	assert.Equal(t, p.Summary(), got.Approvals[0].Summary)
	assert.Equal(t, contractTool+": ls", got.Approvals[0].Summary)
	assert.Equal(t, []string{ownerHarp, child.Harp}, got.Approvals[0].Lineage)

	text := runApprovalsList(t, "text")
	assert.Contains(t, text, child.Harp)
	assert.Contains(t, text, contractTool+": ls")

	pending := lane.c.Approvals().Pending()
	require.Len(t, pending, 1, "listing answered nothing")
	assert.Equal(t, p.ID, pending[0].ID)
	require.NoError(t, lane.c.Approvals().Answer(p.ID, coord.ApprovalDecision{Allow: true, Decider: agent.DeciderHuman}))
}
