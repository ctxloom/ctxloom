package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/internal/docsgen"
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

	// `bundle` roots ~30 of the checked-in reference pages, so it must survive
	// PrepareTree visible. It is no longer hidden in internal/cli, so this is a
	// characterization of the ordinary case; the hidden-command decision itself
	// is enforced by TestEveryHiddenTopLevelCommandIsDecided.
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
		t.Error("bundle is hidden after PrepareTree — its pages would not generate")
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
	code := run(&out, []string{}, ctxloomProduct)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "at least one of --man") {
		t.Errorf("the entrypoint said nothing about why it failed; stderr = %q", out.String())
	}
}

// TestRun_ReportsAnAssemblyFailureInsteadOfPanicking pins U156-F04. The row's
// premise — that ctxloomProduct calls cli.NewDocMCPServer, which PANICS on two
// internal failure paths, so a bug in the runner MCP assembly surfaces as a
// panic from a function whose signature promises no failure — was true at the
// census and is already fixed, under a SIBLING finding's ID: 2e9df890
// "fix(U038-F15): release the docgen Home and return its failures" gave
// NewDocMCPServer an error return and threaded it through ctxloomProduct.
//
// What that fix did not ship is any check that the entrypoint DOES something
// with the error it now receives. An assembly failure cannot be provoked from
// outside (the dead endpoint is a constant and a routing mismatch is a build
// defect), so run takes its product constructor as a parameter and this
// supplies one that fails.
func TestRun_ReportsAnAssemblyFailureInsteadOfPanicking(t *testing.T) {
	failing := func() (*docsgen.Product, func(), error) {
		return nil, nil, errors.New("docgen: assemble runner MCP surface: boom")
	}

	var out bytes.Buffer
	code := run(&out, []string{"--mcp", t.TempDir()}, failing)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "assemble runner MCP surface") {
		t.Errorf("the assembly failure was not reported; stderr = %q", out.String())
	}
}

// pristineHidden records every top-level command's Hidden flag exactly as
// internal/cli declares it, snapshotted at package init. It has to be a
// snapshot: cli.GetRootCmd() returns a process-wide singleton and PrepareTree
// MUTATES it, so a test that reads c.Hidden directly is reading whatever an
// earlier test in the same binary left behind.
var pristineHidden = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range cli.GetRootCmd().Commands() {
		m[c.Name()] = c.Hidden
	}
	return m
}()

// undocumentedHidden are the top-level commands hidden from --help that are
// also deliberately NOT documented: shell plumbing (completion), hook
// endpoints ctxloom invokes on the user's behalf (hook), and internal helpers
// (plan, util). They are the complement of Product.Unhide, and together the two
// sets must account for every hidden top-level command.
var undocumentedHidden = map[string]bool{
	"completion": true,
	"hook":       true,
	"plan":       true,
	"util":       true,
}

// TestEveryHiddenTopLevelCommandIsDecided pins U156-F05. `Unhide` is a
// hand-maintained list in THIS package keyed on a `Hidden` flag set in
// internal/cli, and the two are joined by nothing. The coupling fails in both
// directions and both are silent: a command newly marked `Hidden: true` for the
// same "advanced but documented" reason simply loses its reference pages with
// the generator still reporting success, and an Unhide entry for a command that
// is no longer hidden is a no-op nobody notices.
//
// The coupling cannot be removed -- "hidden but documented" versus "hidden and
// undocumented" is a real per-command decision that nothing in the tree
// encodes -- so it is made CHECKED. Every hidden top-level command must be in
// exactly one of the two sets, and every entry in either set must name a
// command that is actually hidden.
func TestEveryHiddenTopLevelCommandIsDecided(t *testing.T) {
	p, closeMCP, err := ctxloomProduct()
	if err != nil {
		t.Fatalf("ctxloomProduct: %v", err)
	}
	t.Cleanup(closeMCP)

	unhide := map[string]bool{}
	for _, n := range p.Unhide {
		unhide[n] = true
	}

	for name, hidden := range pristineHidden {
		if !hidden {
			continue
		}
		if !unhide[name] && !undocumentedHidden[name] {
			t.Errorf("top-level command %q is hidden but appears in neither Product.Unhide nor undocumentedHidden: "+
				"it silently generates no reference pages -- decide which it is", name)
		}
		if unhide[name] && undocumentedHidden[name] {
			t.Errorf("top-level command %q is in BOTH Product.Unhide and undocumentedHidden", name)
		}
	}

	for name := range unhide {
		if _, ok := pristineHidden[name]; !ok {
			t.Errorf("Product.Unhide names %q, which is not a top-level command -- a rename left it dangling", name)
		} else if !pristineHidden[name] {
			t.Errorf("Product.Unhide names %q, which internal/cli does not hide -- the unhide is a no-op and the list has gone stale", name)
		}
	}
	for name := range undocumentedHidden {
		if !pristineHidden[name] {
			t.Errorf("undocumentedHidden names %q, which internal/cli does not hide -- this list has gone stale", name)
		}
	}
}
