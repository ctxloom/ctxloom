package grpc

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCellKind_ProtoRoundTrip pins the wire<->plugin conversion: every
// agent.CellKind survives CellKindToProto→cellKindFromProto unchanged, and the
// UNSPECIFIED wire value decodes to Shared (the documented default for a run
// whose host didn't stamp a cell).
func TestCellKind_ProtoRoundTrip(t *testing.T) {
	for _, k := range []agent.CellKind{
		agent.CellKindShared,
		agent.CellKindDirectoryIsolated,
		agent.CellKindProcessIsolated,
	} {
		assert.Equal(t, k, cellKindFromProto(CellKindToProto(k)), "round-trip for %v", k)
	}
	assert.Equal(t, CellKind_CELL_KIND_SHARED, CellKindToProto(agent.CellKindShared),
		"Shared must stamp the explicit SHARED value, not UNSPECIFIED")
	assert.Equal(t, agent.CellKindShared, cellKindFromProto(CellKind_CELL_KIND_UNSPECIFIED),
		"UNSPECIFIED must decode to Shared")
}

// TestGRPCServer_Run_ThreadsCellKind proves the resolved cell flows host→plugin:
// the wire CellKind on RunOptions lands as the mapped agent.CellKind on BOTH the
// SetupRequest and the ExecuteRequest the server builds. SkipSetup is left false
// so Setup runs and its cell is captured too.
func TestGRPCServer_Run_ThreadsCellKind(t *testing.T) {
	cases := []struct {
		name string
		wire CellKind
		want agent.CellKind
	}{
		{"unspecified→shared", CellKind_CELL_KIND_UNSPECIFIED, agent.CellKindShared},
		{"shared", CellKind_CELL_KIND_SHARED, agent.CellKindShared},
		{"directory-isolated", CellKind_CELL_KIND_DIRECTORY_ISOLATED, agent.CellKindDirectoryIsolated},
		{"process-isolated", CellKind_CELL_KIND_PROCESS_ISOLATED, agent.CellKindProcessIsolated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeBackend{name: "capture", executeResult: &agent.ExecuteResult{ExitCode: 0}}
			srv := &GRPCServer{Impl: backend}
			stream := newFakeRunServer()
			stream.recv = []*RunInput{runStartInput(&RunStart{
				Prompt:  &Fragment{Content: "hi"},
				Options: &RunOptions{WorkDir: "/tmp", CellKind: tc.wire},
			})}
			require.NoError(t, srv.Run(stream))
			require.True(t, backend.setupCalled, "Setup must run so its cell is captured")
			assert.Equal(t, tc.want, backend.capturedSetupCell, "cell must land in SetupRequest")
			assert.Equal(t, tc.want, backend.capturedExecuteCell, "cell must land in ExecuteRequest")
		})
	}
}
