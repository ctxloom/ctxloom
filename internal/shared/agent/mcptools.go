package agent

// MCPToolPrefix is how claude namespaces an MCP server's tools:
// mcp__<server>__<tool>. Naming the SERVER alone — mcp__<server> — grants
// every tool that server offers.
const MCPToolPrefix = "mcp__"

// QualifyMCPServer renders a server-level permission rule for one MCP server.
// The result names the server, not a tool, so it grants the whole server.
func QualifyMCPServer(server string) string { return MCPToolPrefix + server }

// QualifyMCPServers renders a server-level rule per name, skipping empties so
// a server with no name cannot emit the bare "mcp__" prefix — which would read
// as a malformed rule rather than a grant of nothing.
func QualifyMCPServers(servers []string) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		if s == "" {
			continue
		}
		out = append(out, QualifyMCPServer(s))
	}
	return out
}
