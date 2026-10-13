package claude

import (
	"errors"
	"maps"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// This file holds the pieces of claude's delivery its typed approaches
// (definition.go) share: the approach names a binding selects
// (ApproachSystemPrompt, ApproachMCPConfig — claude's own, declared here and
// nowhere shared) and the relay bearer's by-reference rewrite for a project
// .mcp.json.

// ApproachSystemPrompt names claude's out-of-cwd framed context consumed via
// --append-system-prompt-file — semantically distinct from the native file
// (the content enters the system prompt, not project memory). It is claude's
// own name, declared here and nowhere shared: no other engine has it.
const ApproachSystemPrompt = "system-prompt"

// ApproachMCPConfig names claude's PRIVATE MCP config file, announced on
// --mcp-config. It is claude's own name, declared here and nowhere shared, and
// it is the DEFAULT: a run's MCP set is ctxloom's to deliver, and delivering it
// by writing the user's project .mcp.json is a shared/dangerous avenue nothing
// should take by default.
const ApproachMCPConfig = "mcp-config"

// relayBearerRef is how the project .mcp.json names the relay's bearer.
var relayBearerRef = "${" + EnvRelayBearer + "}"

// errTwoRelayBearers refuses a server set naming two different relay
// bearers: claude's environment holds one value per name.
var errTwoRelayBearers = errors.New("claude: two MCP entries carry different relay bearers, and claude's environment can hold only one")

// bearerByReference returns bundle with every relay bearer value replaced by
// relayBearerRef, and the value it replaced, keyed by its variable. The
// caller's servers are not modified.
func bearerByReference(bundle map[string]wire.MCPServer) (map[string]wire.MCPServer, map[string]string, error) {
	out := make(map[string]wire.MCPServer, len(bundle))
	var env map[string]string
	for name, srv := range bundle {
		v, ok := srv.Env[EnvRelayBearer]
		if !ok || v == relayBearerRef {
			out[name] = srv
			continue
		}
		if prev, seen := env[EnvRelayBearer]; seen && prev != v {
			return nil, nil, errTwoRelayBearers
		}
		env = map[string]string{EnvRelayBearer: v}
		srv.Env = maps.Clone(srv.Env)
		srv.Env[EnvRelayBearer] = relayBearerRef
		out[name] = srv
	}
	return out, env, nil
}
