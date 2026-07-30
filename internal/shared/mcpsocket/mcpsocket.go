// Package mcpsocket holds the value FORMS of the runner-MCP endpoint address
// (the CTXLOOM_MCP_SOCKET variable, whose own name and lifecycle are documented
// on agentcoord/coord.EnvMCPSocket).
//
// It is a LEAF — no imports of its own, of any kind — because the two sides of
// that address are on opposite sides of a layering boundary and neither may
// import the other: internal/acp's container transport WRITES the value, the
// `ctxloom mcp` forward shim in internal/cli READS it, and internal/cli
// importing internal/acp would breach the one-door invariant (a CLI frontend
// must reach an engine only through pb.Client.Chat — see internal/acptest's
// no-import test). A shared leaf below both is the only home where the contract
// can be stated once instead of hand-copied at each end.
package mcpsocket

// TCPPrefix marks a CTXLOOM_MCP_SOCKET value as the off-Linux TCP form (a
// host:port to dial) rather than the default unix socket path — which is always
// an absolute filesystem path and so never collides with this prefix. Off Linux
// a bind-mounted unix socket file is not a live endpoint across the Docker
// Desktop VM boundary, so the ACP reach-back bridges the runner's socket onto a
// host-loopback TCP port and encodes the result with this marker.
const TCPPrefix = "tcp://"
