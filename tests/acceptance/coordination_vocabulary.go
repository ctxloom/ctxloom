//go:build acceptance

package acceptance

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// The coordination tools' WIRE vocabulary, as the runner-terminated surface
// a real harness receives serves it (proto-canonical, coordination.proto).
// The delegation journeys drive that surface through a forwarding shim, so
// their steps build arguments and read results in these names.
//
// Each name is CHECKED against the proto descriptor at package init: a field
// renamed on the wire fails the suite before any scenario runs, instead of
// letting a step silently read a missing key as "no message".
var (
	spawnChildAgentIDField = protoFieldName(&pb.SpawnAgentResult{}, "child_agent_id")
	spawnChildRunIDField   = protoFieldName(&pb.SpawnAgentResult{}, "child_run_id")
	recvFromAgentIDField   = protoFieldName(&pb.PeerMessage{}, "from_agent_id")
	recvTextField          = protoFieldName(&pb.PeerMessage{}, "text")
)

// protoFieldName returns name after asserting m declares a field by it.
func protoFieldName(m proto.Message, name string) string {
	if m.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name)) == nil {
		panic(fmt.Sprintf("acceptance: %s declares no field %q — the coordination wire vocabulary moved; fix the steps that read it", m.ProtoReflect().Descriptor().FullName(), name))
	}
	return name
}
