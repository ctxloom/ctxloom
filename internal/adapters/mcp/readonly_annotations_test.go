package mcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadOnlyAnnotationsPresent pins that SOMETHING still claims readOnlyHint.
//
// Under a plan posture claude refuses every MCP tool that does not declare the
// hint — measured against claude 2.1.251: an annotated tool succeeded and an
// unannotated one on the same server, under the same server-level grant, still
// returned "Cannot call ... while in plan mode". So if the annotations were
// ever dropped wholesale, a plan agent would silently lose access to every
// ctxloom tool and the only symptom would be refusals that read like a
// misconfigured allowlist.
func TestReadOnlyAnnotationsPresent(t *testing.T) {
	annotated := annotatedReadOnlyToolNames(t)
	require.NotEmpty(t, annotated,
		"no tool carries readOnlyHint; a plan-mode agent would reach none of them")
	// The read-only surface a plan agent actually depends on.
	for _, want := range []string{"search_content", "search_library", "context_status"} {
		assert.Contains(t, annotated, want,
			"%s is read-only and must declare readOnlyHint to survive plan mode", want)
	}
}

// TestReadOnlyAnnotationsExcludeMutatingTools guards the direction that is
// dangerous rather than merely broken.
//
// The grant permissionArgs emits is SERVER-level, so it does not discriminate
// between tools — readOnlyHint is the only thing standing between a plan agent
// and a mutating tool. Annotating one to buy it past that gate would both
// breach the read-only posture AND misdeclare the tool to every other MCP
// client, which reads the same hint for its own reasons.
//
// Each name is asserted PRESENT before it is asserted unannotated. Without
// that first half the check is absence-satisfies-absence: a name this server
// does not register would sail through, reporting a guard over a tool the
// fixture never had. That is not hypothetical here — every coordination
// tool (agent_run, agent_report, agent_fetch_artifact, …) is registered on
// the RUNNER's surface, not this one, so naming one here would prove exactly
// nothing while reading as the strongest line in the test.
func TestReadOnlyAnnotationsExcludeMutatingTools(t *testing.T) {
	mutating := []string{
		"compact_session", "recover_session", "load_session", "evaluate_triggers",
	}
	all := allToolNames(t)
	annotated := annotatedReadOnlyToolNames(t)
	for _, name := range mutating {
		require.Contains(t, all, name,
			"%s is not registered on this surface, so asserting it is unannotated "+
				"would pass vacuously; move it or drop it from this list", name)
		assert.NotContains(t, annotated, name,
			"%s mutates state; annotating it read-only both breaks the plan "+
				"posture and lies to every MCP client", name)
	}
}

// allToolNames lists every tool this surface registers, so a guard can assert
// a name is PRESENT before asserting anything about its annotation.
func allToolNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, tool := range listTools(t) {
		names = append(names, tool.Name)
	}
	return names
}

// annotatedReadOnlyToolNames registers the real tool set on a real server and
// lists it back over an in-memory session, reporting the names whose
// annotations claim readOnlyHint. Going over a SESSION rather than reaching
// into the server is deliberate: it reads what a client is actually told,
// which is the thing claude's plan gate consults.
func annotatedReadOnlyToolNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, tool := range listTools(t) {
		if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
			names = append(names, tool.Name)
		}
	}
	return names
}

// listTools stands the real server up and lists it back over an in-memory
// session. Going over a SESSION rather than reaching into the server is
// deliberate: it reads what a client is actually told, which is the thing
// claude's plan gate consults.
func listTools(t *testing.T) []*mcp.Tool {
	t.Helper()
	ctx := context.Background()

	s := &ctxServer{cfg: testConfig()}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(server)

	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "test"}, nil).
		Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Tools, "the server listed no tools at all")
	return res.Tools
}
