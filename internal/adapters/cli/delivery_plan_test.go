package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/launch"
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

// TestUnsafeLabels_TheHostHomeSelectionIsUnsafe: a launch whose binding
// selected the real engine home (`engine_home: host`) is named unsafe
// beside any project route — the two selections are the two ways a run
// writes outside its session, and neither is a default.
func TestUnsafeLabels_TheHostHomeSelectionIsUnsafe(t *testing.T) {
	assert.Equal(t, []string{"mcp → project-root", "engine-home → host"},
		unsafeLabels(launch.Launch{Plan: twoRoutePlan(), Cell: launch.Cell{HomeMode: launch.HomeModeHost}}))
	assert.Equal(t, []string{"engine-home → host"},
		unsafeLabels(launch.Launch{Cell: launch.Cell{HomeMode: launch.HomeModeHost}}))
	assert.Empty(t, unsafeLabels(launch.Launch{
		Plan: delivery.Plan{Static: []delivery.StaticItem{{Kind: present.Context, Root: present.RootSessionHome}}},
		Cell: launch.Cell{HomeMode: launch.HomeModeSession},
	}))
}

// TestEngineHomeRoute_And_PrintNamesTheHostSelectionUnsafe: the dry-run's
// engine-home line on the wire and in text — "session" plain, "host"
// unsafe, and the text form says so once.
func TestEngineHomeRoute_And_PrintNamesTheHostSelectionUnsafe(t *testing.T) {
	assert.Equal(t, engineHomeJSON{Mode: "session", Unsafe: false}, engineHomeRoute(launch.HomeModeSession))
	assert.Equal(t, engineHomeJSON{Mode: "host", Unsafe: true}, engineHomeRoute(launch.HomeModeHost))

	var buf bytes.Buffer
	printEngineHome(&buf, engineHomeRoute(launch.HomeModeHost))
	assert.Contains(t, buf.String(), "engine-home → host  (unsafe")
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("unsafe")))

	buf.Reset()
	printEngineHome(&buf, engineHomeRoute(launch.HomeModeSession))
	assert.Contains(t, buf.String(), "engine-home → session\n")
	assert.NotContains(t, buf.String(), "unsafe")
}
