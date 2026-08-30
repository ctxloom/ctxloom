package grpc

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestRunOptions_EmptyFrameCharacterization records what an EMPTY or truncated
// RunOptions frame actually becomes at the decode boundary. It is a
// characterization test: green before and after, asserting nothing about what
// the behaviour ought to be, only fixing what it currently is so the question
// stays answerable.
//
// It exists because only one scalar field in llm.proto is proto3 `optional`,
// so absent and zero are indistinguishable everywhere else, which means an
// empty frame "decodes as a valid, permissive request". The first half is a
// property of proto3 and is
// asserted here directly. The second half is the part worth pinning, because
// the permissiveness runs BOTH ways field by field, and no wire-format change
// can be judged without knowing which way each one falls.
func TestRunOptions_EmptyFrameCharacterization(t *testing.T) {
	// Absent and all-defaults are literally the same bytes: proto3 does not
	// encode a zero-valued non-optional scalar, so there is no frame a peer
	// could send that means "I did not set these".
	wire, err := proto.Marshal(&RunOptions{})
	require.NoError(t, err)
	assert.Empty(t, wire, "an all-zero RunOptions marshals to zero bytes")

	exec := turnExecuteRequest(&RunStart{Options: &RunOptions{}}, "", nil, nil, nil, nil)

	// The safety-relevant fields decode to the CONSERVATIVE value, not the
	// permissive one.
	assert.Equal(t, agent.PermissionDefault, exec.Permissions,
		"an absent permission_mode decodes to the prompting posture")
	assert.False(t, exec.Permissions.AllowsWithoutPrompt(),
		"an absent permission_mode never allows a tool without asking")
	assert.Equal(t, agent.ModeInteractive, exec.Mode,
		"an absent mode decodes to interactive — a human is assumed present")
	assert.Equal(t, agent.CellKindShared, exec.CellKind,
		"an absent cell decodes to the shared cwd; the plugin does not create isolation, it is told where it already is")

	// The fields where absence IS the permissive answer.
	assert.False(t, exec.DryRun, "an absent dry_run means EXECUTE, not validate")
	assert.False(t, exec.SkipSetup, "an absent skip_setup means full setup runs")
	assert.Empty(t, exec.WorkDir, "an absent work_dir is empty, not a resolved default")

	// The one combination that does auto-approve requires an EXPLICIT non-zero
	// field, so a truncated frame cannot reach it: the headless floor upgrades a
	// prompting posture to bypass only once mode is explicitly ONESHOT, because
	// a oneshot has no human to answer the engine and would otherwise hang.
	oneshot := turnExecuteRequest(&RunStart{Options: &RunOptions{Mode: ExecutionMode_ONESHOT}}, "", nil, nil, nil, nil)
	assert.Equal(t, agent.PermissionBypass, oneshot.Permissions,
		"the headless floor is reached only via an explicitly-set mode, never by truncation")
}
