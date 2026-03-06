// Package operations provides the shared business logic for SCM tools.
//
// This package serves as the single source of truth for all SCM operations,
// with both MCP tools and CLI commands acting as thin adapters that call
// these operations with appropriate input/output formatting.
//
// # Design Principles
//
//   - Operations take *config.Config as a parameter for project context
//   - Operations return structs that are JSON-serializable
//   - No output formatting in operations - adapters handle presentation
//   - Operations wrap existing packages (remote/, bundles/, profiles/)
//
// # Usage
//
// MCP tools unmarshal JSON parameters and call operations:
//
//	func (s *mcpServer) toolListRemotes(args json.RawMessage) (interface{}, error) {
//	    var req operations.ListRemotesRequest
//	    json.Unmarshal(args, &req)
//	    return operations.ListRemotes(s.ctx, s.cfg, req)
//	}
//
// CLI commands parse flags and call the same operations:
//
//	func runRemoteList(cmd *cobra.Command, args []string) error {
//	    cfg, _ := config.Load()
//	    result, _ := operations.ListRemotes(cmd.Context(), cfg, operations.ListRemotesRequest{})
//	    // Format result for human output
//	    for _, r := range result.Remotes {
//	        fmt.Printf("  %s  %s\n", r.Name, r.URL)
//	    }
//	    return nil
//	}
package operations
