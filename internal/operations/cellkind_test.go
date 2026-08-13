package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestCellKindForPolicy pins the host-side mapping the run boundary stamps onto
// RunOptions.CellKind: each resolved isolation.Policy maps to the expected
// agent.CellKind (None→Shared, Worktree→DirectoryIsolated, and BOTH container
// base tiers → ProcessIsolated), and the value survives the stamp conversion to
// the wire enum. The constructors are exercised for Name() only, which is all
// CellKindForPolicy inspects, so nil runtime/git are safe here.
func TestCellKindForPolicy(t *testing.T) {
	cases := []struct {
		name      string
		policy    isolation.Policy
		want      agent.CellKind
		wantProto pb.CellKind
	}{
		{"none→shared", isolation.None{}, agent.CellKindShared, pb.CellKind_CELL_KIND_SHARED},
		{"worktree→directory-isolated", isolation.NewWorktree(nil, ""), agent.CellKindDirectoryIsolated, pb.CellKind_CELL_KIND_DIRECTORY_ISOLATED},
		{"container→process-isolated", isolation.NewContainerFor(nil, "mock").WithImage("img"), agent.CellKindProcessIsolated, pb.CellKind_CELL_KIND_PROCESS_ISOLATED},
		{"container-worktree→process-isolated", isolation.NewContainerWorktreeFor(nil, "mock", isolation.ImageConfig{Image: "img"}, nil), agent.CellKindProcessIsolated, pb.CellKind_CELL_KIND_PROCESS_ISOLATED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CellKindForPolicy(tc.policy)
			assert.Equal(t, tc.want, got, "policy %q → agent.CellKind", tc.policy.Name())
			assert.Equal(t, tc.wantProto, pb.CellKindToProto(got), "stamped wire value for %q", tc.policy.Name())
		})
	}
}
