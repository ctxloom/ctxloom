package coord

import (
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// serveWire is the plane-2 path a test drives by hand: the wire request
// decoded, dispatched to its verb, and the reply encoded — what the run
// channel handler does per frame, without a stream.
func serveWire(c *Coordinator, caller Identity, req *agentcoordpb.AgentRequest) *agentcoordpb.CoordinatorResponse {
	decoded, err := AgentRequestFromWire(req)
	if err != nil {
		return AgentReplyToWire(AgentReply{RequestID: decoded.RequestID, Err: decodeRefusal(err)})
	}
	reply := c.serveAgentRequest(caller, decoded)
	reply.RequestID = decoded.RequestID
	return AgentReplyToWire(reply)
}
