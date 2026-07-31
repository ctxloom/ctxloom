package main

import (
	"bytes"
	"strings"
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

// TestRun_ReportsAGeneratorFailure pins U156-F03: main discarded Execute's
// error and exited 1 with nothing of its own to say. Cobra does print errors by
// default, so this was not usually silent — but "usually" was the whole
// guarantee: the default belongs to the command this entrypoint is handed, and
// a command with SilenceErrors set (or a generator that returns an error
// without cobra involved) would have exited 1 with no reason given, in a
// build-time tool whose only user is a CI gate reading stderr.
//
// Test-seam row (template §4 class 4): run() did not exist before, so the
// honest pre-fix test does not compile. Demonstrated red with the SEAM PRESENT
// and only the reporting reverted — see the commit body.
func TestRun_ReportsAGeneratorFailure(t *testing.T) {
	var out bytes.Buffer
	// No destination flag: the generator refuses, which is the ordinary
	// Execute-returns-an-error path.
	code := run(&out, []string{})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "at least one of --man") {
		t.Errorf("the entrypoint said nothing about why it failed; stderr = %q", out.String())
	}
}
