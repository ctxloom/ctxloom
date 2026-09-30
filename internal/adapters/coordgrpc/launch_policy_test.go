package coordgrpc_test

import (
	"strings"
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

// The wire enum carries engine.PermissionMode by value: each value's wire
// name is the mode's own.
func TestPermissionMode_WireEnumIsTheEngineVocabulary(t *testing.T) {
	for m := engine.PermissionDefault; m <= engine.PermissionAuto; m++ {
		name := pb.PermissionMode(m).String()
		want := "PERMISSION_MODE_" + strings.ToUpper(map[engine.PermissionMode]string{
			engine.PermissionDefault: "default", engine.PermissionAcceptEdits: "accept_edits", engine.PermissionPlan: "plan",
			engine.PermissionBypass: "bypass", engine.PermissionDontAsk: "dont_ask", engine.PermissionAuto: "auto",
		}[m])
		assert.Equalf(t, want, name, "%s", m)
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
}
