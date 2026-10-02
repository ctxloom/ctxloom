package coordgrpc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// Every field of the policy crosses: a policy field the encoder never
// writes is a rule, an approver or a timeout the runner never honours.
func TestEncodeLaunch_EveryPolicyFieldIsPopulated(t *testing.T) {
	msg := coordgrpc.EncodeLaunch(launchtest.FullLaunch(t)).GetPermission().ProtoReflect()
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		assert.Truef(t, msg.Has(fields.Get(i)), "policy wire field %q is not populated", fields.Get(i).Name())
	}
}

// Every approver and sandbox crosses as itself, and the engine's document
// crosses as JSON reads it back — a rule list's []string as []any, which
// the engine's own reader takes.
func TestLaunchPolicy_RoundTrips(t *testing.T) {
	for _, a := range []engine.Approver{engine.ApproverHuman, engine.ApproverNone, engine.ApproverReviewer} {
		for _, sb := range []engine.Sandbox{engine.SandboxReadOnly, engine.SandboxWorkspaceWrite, engine.SandboxFull} {
			l := launchtest.FullLaunch(t)
			l.Permission.Approver, l.Permission.Sandbox = a, sb
			l.Permission.Posture.Document = map[string]any{"mode": "plan", "deny": []string{"Bash(rm *)"}}
			back, err := coordgrpc.DecodeLaunch(coordgrpc.EncodeLaunch(l))
			require.NoError(t, err)
			want := l.Permission
			want.Posture.Document = map[string]any{"mode": "plan", "deny": []any{"Bash(rm *)"}}
			assert.Equalf(t, want, back.Permission, "%s %s", a, sb)
		}
	}
}

// Nobody resolved an approver the wire does not name: the decoder refuses
// rather than guess whether the human is asked.
func TestDecodeLaunch_RefusesAnUnspecifiedApprover(t *testing.T) {
	wire := coordgrpc.EncodeLaunch(launchtest.FullLaunch(t))
	wire.GetPermission().Approver = pb.Approver_APPROVER_UNSPECIFIED
	_, err := coordgrpc.DecodeLaunch(wire)
	require.Error(t, err)

	wire = coordgrpc.EncodeLaunch(launchtest.FullLaunch(t))
	wire.Permission = nil
	_, err = coordgrpc.DecodeLaunch(wire)
	require.Error(t, err, "a launch with no policy is refused")

	wire = coordgrpc.EncodeLaunch(launchtest.FullLaunch(t))
	wire.GetPermission().Sandbox = pb.Sandbox_SANDBOX_UNSPECIFIED
	_, err = coordgrpc.DecodeLaunch(wire)
	require.ErrorContains(t, err, "sandbox", "nobody guesses what bounds the engine")

	wire = coordgrpc.EncodeLaunch(launchtest.FullLaunch(t))
	wire.GetPermission().Engine = ""
	_, err = coordgrpc.DecodeLaunch(wire)
	require.ErrorContains(t, err, "engine", "a posture names the engine that reads it")
}
