package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Input types for fragment tools. Fields without "omitempty" are required;
// fields with "omitempty" are optional. The SDK's schema generator reads
// the json tag for the property name and the jsonschema tag for the
// description that surfaces in tools/list to the client.

type listFragmentsInput struct {
	Query     string   `json:"query,omitempty" jsonschema:"Text search on name"`
	Tags      []string `json:"tags,omitempty" jsonschema:"Filter by tags"`
	SortBy    string   `json:"sort_by,omitempty" jsonschema:"Sort field (one of: name, source; default: name)"`
	SortOrder string   `json:"sort_order,omitempty" jsonschema:"Sort order (one of: asc, desc; default: asc)"`
}

type getFragmentInput struct {
	Name string `json:"name" jsonschema:"Fragment name (without extension)"`
}

type createFragmentInput struct {
	Name    string   `json:"name" jsonschema:"Fragment name (without extension)"`
	Content string   `json:"content" jsonschema:"Fragment content (markdown)"`
	Tags    []string `json:"tags,omitempty" jsonschema:"Tags for the fragment"`
	Version string   `json:"version,omitempty" jsonschema:"Version string (default: 1.0)"`
}

type deleteFragmentInput struct {
	Name string `json:"name" jsonschema:"Fragment name to delete"`
}

// deleteFragmentResult mirrors the legacy {"status": "deleted"} response
// shape so existing clients keep working through the SDK migration. The
// operations.DeleteFragmentResult type isn't surfaced because the legacy
// handler intentionally returned only the status string.
type deleteFragmentResult struct {
	Status string `json:"status"`
}

// registerFragmentTools is a no-op stub after Phase 4.
//
//   Lever A (listings → resources):
//     list_fragments → ctxloom://fragments
//     get_fragment   → ctxloom://fragments/{name}
//   Lever B (writes → CLI):
//     create_fragment → ctxloom fragment create <bundle> <name>
//     delete_fragment → ctxloom fragment delete <ref>
func (s *ctxServer) registerFragmentTools(server *mcp.Server) {
	_ = server
}
