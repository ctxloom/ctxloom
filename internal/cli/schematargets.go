//go:build schemagen

package cli

import (
	"reflect"

	"github.com/ctxloom/ctxloom/internal/schemagen"
)

// SchemaTargets lists the JSON output structs that live in package cmd — the MCP
// tool result shapes and the SessionStart hook output. They are unexported, so
// the schema generator cannot name them from outside; this in-package, build-
// tagged list is how cmd/gen-schemas reaches them. Names are prefixed "mcp-" so
// they don't collide with the operations.* result of the same base name.
func SchemaTargets() []schemagen.Target {
	return []schemagen.Target{
		{Type: reflect.TypeOf(compactSessionResult{}), Name: "mcp-compact-session-result"},
		{Type: reflect.TypeOf(loadSessionResult{}), Name: "mcp-load-session-result"},
		{Type: reflect.TypeOf(HookOutput{}), Name: "hook-output"},
	}
}
