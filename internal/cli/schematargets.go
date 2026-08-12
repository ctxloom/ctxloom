//go:build schemagen

package cli

import (
	"reflect"

	"github.com/ctxloom/ctxloom/internal/schemagen"
)

// SchemaTargets lists the JSON output structs that live in this package — the
// SessionStart hook output. It is unexported, so the schema generator cannot
// name it from outside; this in-package, build-tagged list is how
// cmd/gen-schemas reaches it. The MCP tool result shapes moved out with the
// rest of the MCP implementation and publish their own list
// (internal/mcp.SchemaTargets).
func SchemaTargets() []schemagen.Target {
	return []schemagen.Target{
		{Type: reflect.TypeOf(HookOutput{}), Name: "hook-output"},
	}
}
