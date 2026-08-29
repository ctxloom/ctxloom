package grpc_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file proves the LAUNCH PATH end to end for the mock engine, through the
// exact function every live `ctxloom run` turn goes through (pb.RunTurn — the
// shared turn body the plugin's Run RPC and the in-process path both call).
//
// The claim it pins is NOT "Mock.Setup can deliver" (mock_launch_delivery_test.go
// in internal/lm/backends pins that). It is "the turn actually reaches that
// delivery" — the half that was false while Mock overrode Setup with a
// stash-and-return-nil bypass, so the context route existed and no run ever
// called it.
//
// It lives in the EXTERNAL test package (grpc_test) deliberately: package grpc
// cannot import internal/lm/backends (backends -> acp -> lm/isolation -> lm/grpc
// is a real cycle), and a fake backend here would prove nothing about the
// backend under test.

// blockingStdin drives the mock's interactive echo mode (CTXLOOM_MOCK_ECHO_STDIN)
// so the OBSERVER runs while the turn is mid-flight: Setup has completed and
// Cleanup has not. Its Read is the assertion point, which makes the observation
// deterministic — no sleeps, no polling, no race with the turn's teardown.
type blockingStdin struct {
	once    sync.Once
	observe func()
	rest    io.Reader
	// ran records that the observation actually happened. A test whose
	// observer never fires would see the zero value of whatever it collected
	// and could pass vacuously — the exact failure mode a negative control is
	// supposed to be immune to, so it is asserted rather than assumed.
	ran bool
}

func (s *blockingStdin) Read(p []byte) (int, error) {
	s.once.Do(func() {
		s.observe()
		s.ran = true
	})
	return s.rest.Read(p)
}

// TestRunTurn_MockDeliversContextSurfaceDuringTheTurn is the end-to-end
// delivery proof: a full turn (Setup -> Execute -> Cleanup) over the real Mock
// backend must have MOCK_CONTEXT.md on disk, carrying the run's own fragment
// bytes, at the moment the engine is executing — i.e. at the moment an engine
// would read it.
//
// The assertion is on BYTES observed mid-turn. Asserting after RunTurn returns
// would be vacuous in the opposite direction: the shared LIFO Cleanup strips
// the managed section at teardown (same as claude's CLAUDE.md), so the
// file is legitimately gone by then — which the second half of this test pins
// as well, so "nothing on disk afterwards" can never be mistaken for "nothing
// was ever delivered".
func TestRunTurn_MockDeliversContextSurfaceDuringTheTurn(t *testing.T) {
	dir := t.TempDir()
	contextPath := filepath.Join(dir, "MOCK_CONTEXT.md")

	var midTurn []byte
	var midTurnErr error
	stdin := &blockingStdin{
		observe: func() { midTurn, midTurnErr = os.ReadFile(contextPath) },
		rest:    strings.NewReader("quit\n"),
	}

	var out strings.Builder
	res, err := pb.RunTurn(context.Background(), backends.NewMock(), &pb.RunStart{
		Fragments: []*pb.Fragment{{Content: "TURN-MARKER-2f7c"}},
		Options: &pb.RunOptions{
			WorkDir:  dir,
			Mode:     pb.ExecutionMode_INTERACTIVE,
			CellKind: pb.CellKindToProto(agent.CellKindShared),
			// The mock's interactive echo mode: it blocks on stdin, which is
			// what holds the turn open across the observation above.
			Env: map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		},
		// The host ships this on every non-skip-setup run
		// (backends.AssembleManagedConfig, via cli/run.go's buildRunRequest);
		// the cells seam short-circuits on a nil one.
		ManagedConfig: pb.ManagedConfigToProto(&agent.ManagedConfig{Hooks: &wire.HooksConfig{}}),
	}, stdin, &out, &out, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.True(t, stdin.ran, "the observation never ran; the assertions below would be vacuous")
	require.NoError(t, midTurnErr, "the turn must have materialized %s before the engine ran", contextPath)
	assert.Contains(t, string(midTurn), "TURN-MARKER-2f7c",
		"the turn's own fragment bytes must reach the delivered file, verbatim")
	assert.Contains(t, string(midTurn), agent.ManagedContextBegin,
		"the content must sit inside the ctxloom-managed markers")

	// And the shared teardown reversed it: no debris left in the workspace.
	_, statErr := os.Stat(contextPath)
	assert.True(t, os.IsNotExist(statErr),
		"the turn's Cleanup must reverse the delivery, got stat err %v", statErr)
}

// TestRunTurn_MockSkipSetup_DeliversNothing is the negative control. A
// skip-setup turn (headless distill, the shared-cwd oneshot member path) runs no
// Setup at all, so nothing may be written into the workspace. Without it, the
// test above could pass for a backend that delivered from somewhere other than
// the turn's Setup.
func TestRunTurn_MockSkipSetup_DeliversNothing(t *testing.T) {
	dir := t.TempDir()

	var seen []os.DirEntry
	stdin := &blockingStdin{
		observe: func() { seen, _ = os.ReadDir(dir) },
		rest:    strings.NewReader("quit\n"),
	}

	var out strings.Builder
	_, err := pb.RunTurn(context.Background(), backends.NewMock(), &pb.RunStart{
		Fragments: []*pb.Fragment{{Content: "NEVER-DELIVERED-2f7c"}},
		Options: &pb.RunOptions{
			WorkDir:   dir,
			Mode:      pb.ExecutionMode_INTERACTIVE,
			SkipSetup: true,
			CellKind:  pb.CellKindToProto(agent.CellKindShared),
			Env:       map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		},
		ManagedConfig: pb.ManagedConfigToProto(&agent.ManagedConfig{Hooks: &wire.HooksConfig{}}),
	}, stdin, &out, &out, nil, nil)
	require.NoError(t, err)

	require.True(t, stdin.ran, "the observation never ran; the assertion below would be vacuous")
	assert.Empty(t, seen, "a skip-setup turn must write nothing into the workspace")
}
