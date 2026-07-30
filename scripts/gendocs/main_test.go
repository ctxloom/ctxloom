package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestCtxloomProduct pins the wiring this entrypoint is responsible for: the
// real ctxloom command tree, a documentation MCP server, and the two site/man
// conventions the checked-in pages were generated under. The generator behaviour
// itself is tested in internal/docsgen.
func TestCtxloomProduct(t *testing.T) {
	p, closeMCP, err := ctxloomProduct()
	if err != nil {
		t.Fatalf("ctxloomProduct: %v", err)
	}
	t.Cleanup(closeMCP)

	if p.Bin != "ctxloom" {
		t.Errorf("Bin = %q, want ctxloom", p.Bin)
	}
	if p.Root == nil || p.Root.Name() != "ctxloom" {
		t.Fatalf("Root is not the ctxloom command tree: %v", p.Root)
	}
	if p.MCPServer == nil {
		t.Error("no MCP server: the MCP reference would not generate")
	}
	if p.LinkBase != "/reference/cli/" {
		t.Errorf("LinkBase = %q — the checked-in pages link through /reference/cli/", p.LinkBase)
	}
	if p.ConfigSchema == "" {
		t.Error("no ConfigSchema: the config reference would not generate")
	}

	// `bundle` is hidden from --help; without the unhide it silently loses its
	// ~30 reference pages.
	var bundle *cobra.Command
	for _, c := range p.Root.Commands() {
		if c.Name() == "bundle" {
			bundle = c
		}
	}
	if bundle == nil {
		t.Fatal("bundle command not found in the ctxloom tree")
	}
	p.PrepareTree()
	if bundle.Hidden {
		t.Error("bundle still hidden after PrepareTree — its pages would not generate")
	}
}
