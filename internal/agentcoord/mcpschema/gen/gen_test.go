package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

// fakeProjector stands in for the descriptor-backed projector: the guard under
// test is about the BINDING TABLE being empty, which needs no descriptors.
type fakeProjector struct{}

func (fakeProjector) ProjectTool(b mcpschema.Binding) (*mcpschema.ToolSpec, error) {
	return &mcpschema.ToolSpec{Name: b.Tool}, nil
}

// U027-F01: the generator could complete having written ZERO files, printing
// nothing and exiting 0 — its only stdout line was inside the loop, so a
// generator that did nothing could not say so. These schemas are checked in and
// embedded as the live MCP tool surface, so an empty run means the binary's tool
// surface silently goes away.
func TestGenerateSchemas_NoBindingsIsAnError(t *testing.T) {
	out := filepath.Join(t.TempDir(), "schemas")
	n, err := generateSchemas(fakeProjector{}, nil, out)
	if err == nil {
		t.Fatalf("an empty binding table must not report success (wrote %d)", n)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a refused run must not leave an output directory behind")
	}
}

func TestGenerateSchemas_WritesOnePerBinding(t *testing.T) {
	out := filepath.Join(t.TempDir(), "schemas")
	n, err := generateSchemas(fakeProjector{}, []mcpschema.Binding{{Tool: "agent_run"}, {Tool: "agent_send"}}, out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("wrote %d, want 2", n)
	}
	for _, name := range []string{"agent_run.json", "agent_send.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}
