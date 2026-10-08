package kit

import (
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// MCPTable is one engine's MCP server table in its config file: the
// top-level key the table lives under and how one server is spelled there.
// The engine supplies the dialect (Entry); the claims are the table's.
type MCPTable struct {
	// Key is the table's top-level member ("mcpServers", "mcp", ...).
	Key string
	// Entry is one server as the engine's config spells it.
	Entry func(name string, s wire.MCPServer) (any, error)
	// Empty, when set, is claimed as the whole table when there is no
	// server: the engine needs the table present even with nothing in it.
	// nil claims nothing then.
	Empty any
}

// Claims is each server as a claim on its own entry under the table, in name
// order, through the bundle that shipped it (wire.MCPServer.SCM); with no
// server, the Empty table when the engine declares one.
func (t MCPTable) Claims(servers map[string]wire.MCPServer) ([]present.Claim, error) {
	claims := make([]present.Claim, 0, len(servers))
	for _, name := range collections.SortedKeys(servers) {
		v, err := t.Entry(name, servers[name])
		if err != nil {
			return nil, err
		}
		claims = append(claims, present.Claim{Pointer: present.PointerKey(t.Key) + present.PointerKey(name), Value: v, Via: servers[name].SCM})
	}
	if len(claims) == 0 && t.Empty != nil {
		claims = append(claims, present.Claim{Pointer: present.PointerKey(t.Key), Value: t.Empty})
	}
	return claims, nil
}
