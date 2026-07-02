package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestAutoApproveForRun pins the STAGE 1 rule the top-level run applies:
// AutoApprove = (mode == ONESHOT) || approvals == ApprovalsBypass. That the
// none/worktree policies declare ApprovalsPrompt and container declares
// ApprovalsBypass is asserted in the isolation package (isolation_test.go /
// worktree_test.go / container_test.go); here we pin the mapping to AutoApprove.
func TestAutoApproveForRun(t *testing.T) {
	cases := []struct {
		name      string
		mode      pb.ExecutionMode
		approvals isolation.Approvals
		want      bool
	}{
		// Interactive + none: claude/etc. now PROMPT (no bypass) — the behaviour
		// change the user approved.
		{"interactive+none prompts", pb.ExecutionMode_INTERACTIVE, isolation.None{}.Approvals(), false},
		// Interactive + a real boundary (container) bypasses the in-engine prompt.
		{"interactive+bypass auto-approves", pb.ExecutionMode_INTERACTIVE, isolation.ApprovalsBypass, true},
		// Any ONESHOT auto-approves regardless of the approvals axis (no human to
		// answer a prompt — it would hang).
		{"oneshot+prompt still auto-approves", pb.ExecutionMode_ONESHOT, isolation.ApprovalsPrompt, true},
		{"oneshot+bypass auto-approves", pb.ExecutionMode_ONESHOT, isolation.ApprovalsBypass, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, autoApproveForRun(tc.mode, tc.approvals))
		})
	}
}
