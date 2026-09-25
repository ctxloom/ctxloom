package mcpschema_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The agent_steer schema tells a calling model which values `delivery` can
// take. Those values are coord's exported Delivery* constants; this pins the
// model-facing text to them, so a constant added, removed or renamed fails here
// instead of leaving the schema describing values that no longer exist.
func TestAgentSteerSchema_NamesEveryDeliveryValue(t *testing.T) {
	values := coordDeliveryValues(t)
	require.NotEmpty(t, values, "no exported Delivery* string constants found in internal/core/coord")

	raw, err := os.ReadFile("schemas/agent_steer.json")
	require.NoError(t, err)
	var schema struct {
		OutputSchema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		} `json:"outputSchema"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	desc := schema.OutputSchema.Properties["delivery"].Description

	for name, v := range values {
		require.Containsf(t, desc, strconv.Quote(v),
			"agent_steer's delivery description must name coord.%s (%q); edit ControlSteerResult.delivery's comment in coordination.proto and regenerate", name, v)
	}
	require.Equalf(t, len(values), strings.Count(desc, `"`)/2,
		"agent_steer's delivery description names a value that is not a coord Delivery* constant: %s", desc)
}

// coordDeliveryValues reads coord's source for its exported Delivery* string
// constants: Go cannot enumerate constants at run time, and a hand-kept list
// here would be the unchecked copy this test exists to replace.
func coordDeliveryValues(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("../../../core/coord/*.go")
	require.NoError(t, err)
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, id := range vs.Names {
				if !id.IsExported() || !strings.HasPrefix(id.Name, "Delivery") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					out[id.Name] = v
				}
			}
			return true
		})
	}
	return out
}
