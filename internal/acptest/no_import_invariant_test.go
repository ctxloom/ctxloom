package acptest

// TestNoImportInvariant_ACPClientOnlyFromPluginTree pins the "one door"
// architecture from the OTHER side of the wire (see the H1b equivalence test,
// internal/cli/acp_door_equivalence_test.go, for the pb.Client.Chat side).
//
// internal/acp is ctxloom's ACP CLIENT: the code that spawns and drives an
// engine subprocess over the Agent Client Protocol. Today it is reachable
// from exactly two kinds of caller: the plugin tree's own per-engine backend
// packages (internal/claude, internal/codex, internal/kiro, internal/opencode
// — each embeds internal/acp.NewChatDriver behind its OWN agent.Backend, so
// their native config/materialization stays correct — see internal/acp/doc.go)
// and internal/lm/backends' registry (which registers the generic "acp"
// backend directly). Nothing outside that set imports it: no CLI frontend, no
// operations-layer session opener, no ACP AGENT-side package (internal/acpagent)
// reaches an engine by spawning one itself — every one of them goes through
// pb.Client.Chat, the one door, instead.
//
// If a future change makes some OTHER package import internal/acp directly,
// that package has grown its OWN engine-spawning door around the shared
// substrate — exactly the "frontend grew its own door" regression the H1b
// equivalence test polices from the consuming side. This test polices it from
// the producing side: it fails the moment an unexpected package imports
// internal/acp at all, independent of whether its behavior happens to still
// agree with the one door (a drift here is a standing invitation for the
// NEXT change to actually diverge).
//
// Uses `go list -json` rather than grep: its per-package "Imports" field is
// PRODUCTION imports only (test files' imports land in a separate
// "TestImports"/"XTestImports" field), so a test file importing internal/acp
// for legitimate test purposes never trips this invariant — exactly the
// distinction that matters here.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// targetACPImport is the import path the invariant restricts.
const targetACPImport = "github.com/ctxloom/ctxloom/internal/acp"

// acpClientAllowedImporters names every package PERMITTED to import
// internal/acp in production code today (the plugin tree: internal/lm/**
// plus the per-engine backend packages internal/lm/backends' registry wires
// to it). internal/acp itself (and its own subpackages, e.g. jsonrpc) is
// excluded from the check entirely, not listed here.
var acpClientAllowedImporters = []string{
	"github.com/ctxloom/ctxloom/internal/lm/backends",
	"github.com/ctxloom/ctxloom/internal/claude",
	"github.com/ctxloom/ctxloom/internal/codex",
	"github.com/ctxloom/ctxloom/internal/kiro",
	"github.com/ctxloom/ctxloom/internal/opencode",
}

// goListPackage is the subset of `go list -json` output this test reads.
// Imports carries PRODUCTION (non-test) imports only.
type goListPackage struct {
	ImportPath string
	Imports    []string
}

func TestNoImportInvariant_ACPClientOnlyFromPluginTree(t *testing.T) {
	root := acptestModuleRoot(t)

	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "go list -json ./... failed: %s", stderr.String())

	var offenders []string
	dec := json.NewDecoder(&stdout)
	for dec.More() {
		var pkg goListPackage
		require.NoError(t, dec.Decode(&pkg))

		if pkg.ImportPath == targetACPImport || strings.HasPrefix(pkg.ImportPath, targetACPImport+"/") {
			continue // the package itself and its own subpackages are not "importers"
		}
		if !slices.Contains(pkg.Imports, targetACPImport) {
			continue
		}
		if slices.Contains(acpClientAllowedImporters, pkg.ImportPath) {
			continue
		}
		offenders = append(offenders, pkg.ImportPath)
	}

	sort.Strings(offenders)
	require.Empty(t, offenders,
		"these package(s) import internal/acp (the ACP CLIENT) directly, outside the plugin tree — "+
			"a frontend has grown its own engine-spawning door instead of going through pb.Client.Chat: %v", offenders)
}

// acptestModuleRoot walks up from the current working directory to find the
// module root (go.mod) — go list -json ./... must run there to enumerate the
// whole module, not just this package.
func acptestModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked up to filesystem root without finding go.mod")
		dir = parent
	}
}
