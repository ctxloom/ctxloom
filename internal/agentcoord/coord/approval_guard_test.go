package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestServeApproval_NonChildCallerRefused pins the !caller.IsChild() guard
// that opens serveApproval (approval.go) — today it exists only in prose, in
// the guard's own inline comment and in owner_run.go's doc comment ("only a
// caller WHERE IsChild() is true may raise one"). No test asserted it at
// all before this one.
//
// This guard is SECURITY-RELEVANT: it is what stops a non-child caller from
// asking the coordinator for an approval decision on someone else's behalf.
// It deserves a test that fails loudly if someone relaxes it casually — see
// the mutation in this test's accompanying commit, which inverts the guard
// and confirms this test goes red.
//
// The guard fires before any run-record lookup, so this calls serveApproval
// directly with a depth-0 (non-child) caller and no run ever having been
// started — proving the refusal is unconditional on that axis alone.
func TestServeApproval_NonChildCallerRefused(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	caller := Identity{Harp: "not-a-child", RunID: "run-does-not-exist", Depth: 0}
	require.False(t, caller.IsChild(), "test setup: this caller must NOT be a child")
	req := &agentcoordpb.ApprovalRequest{
		Kind:  agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION,
		Title: "bash",
	}

	resp := c.serveApproval(caller, req)
	require.NotNil(t, resp)
	assert.Equal(t, int32(codes.PermissionDenied), resp.GetStatus().GetCode(),
		"a non-child caller's ApprovalRequest must be refused with PermissionDenied")
	assert.Contains(t, resp.GetStatus().GetMessage(), "only a delegated child's run asks the coordinator for a decision",
		"the refusal must name WHY, not just fail silently")
}
