package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

func twoRoutePlan() delivery.Plan {
	return delivery.Plan{Static: []delivery.StaticItem{
		{Kind: present.Context, Approach: "context-file", Root: present.RootSessionHome},
		{Kind: present.MCP, Approach: "mcp-config", Root: present.RootProjectRoot},
	}}
}

// TestDeliveryRoutes_ProjectRootIsUnsafe_SessionHomeIsNot: the plan's
// routes carry the unsafe mark exactly on the project-root and work-dir
// routes.
func TestDeliveryRoutes_ProjectRootIsUnsafe_SessionHomeIsNot(t *testing.T) {
	routes := deliveryRoutes(twoRoutePlan())
	assert.Equal(t, []routeJSON{
		{Kind: "context", Approach: "context-file", Root: "session-home", Unsafe: false},
		{Kind: "mcp", Approach: "mcp-config", Root: "project-root", Unsafe: true},
	}, routes)
	assert.True(t, unsafeRoot(present.RootWorkDir))
	assert.Equal(t, []string{"mcp → project-root"}, unsafeRouteLabels(twoRoutePlan()))
	assert.Empty(t, unsafeRouteLabels(delivery.Plan{Static: []delivery.StaticItem{{Kind: present.Context, Root: present.RootSessionHome}}}))
}

// TestPrintDeliveryRoutes_NamesTheUnsafeRoute: the text form says "unsafe"
// on the project route and only there.
func TestPrintDeliveryRoutes_NamesTheUnsafeRoute(t *testing.T) {
	var buf bytes.Buffer
	printDeliveryRoutes(&buf, deliveryRoutes(twoRoutePlan()))
	out := buf.String()
	assert.Contains(t, out, "=== Delivery ===")
	assert.Contains(t, out, "context → session-home via context-file\n")
	assert.Contains(t, out, "mcp → project-root via mcp-config  (unsafe")
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("unsafe")))

	buf.Reset()
	printDeliveryRoutes(&buf, nil)
	assert.Contains(t, buf.String(), "(nothing to deliver)")
}
