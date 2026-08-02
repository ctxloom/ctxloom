package schemagen

// These tests police what doc.go CLAIMS about the `schemagen` build tag.
//
// The tag really does keep this package out of the production build. It does
// NOT keep the reflection library out: github.com/modelcontextprotocol/go-sdk's
// mcp package imports github.com/google/jsonschema-go/jsonschema, and ctxloom's
// binary imports that SDK unconditionally — so the reflector is linked into
// every shipped binary whether or not schemagen is built, and go.mod would
// require the module even if this package were deleted.
//
// doc.go used to claim the tag's isolation stopped at the module graph.
// Measurement says it stops earlier than that: at the binary. The two
// assertions below are what keep the corrected prose honest — if the SDK ever
// drops the dependency, the second one fails and doc.go must be revised again
// rather than silently becoming true-by-accident.
//
// Deliberately NOT behind //go:build schemagen: doc.go is the untagged half of
// this package, so its prose must be checkable in an untagged run.
//
// Uses `go list -deps -json`, following internal/acptest's precedent: the
// per-package Imports field is production imports only, so a test-only import
// can never satisfy or trip either assertion.

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// reflectorImport is the reflection library doc.go used to claim the build
	// tag kept out of the production binary.
	reflectorImport = "github.com/google/jsonschema-go/jsonschema"

	// schemagenImport is this package, which the build tag genuinely does keep
	// out of the production binary.
	schemagenImport = "github.com/ctxloom/ctxloom/internal/schemagen"

	// productionBinary is the command doc.go means by "the production binary".
	productionBinary = "./cmd/ctxloom"
)

// TestBuildTag_ExcludesThisPackageButNotItsReflector pins both halves of
// doc.go's corrected statement against the untagged dependency graph of the
// shipped command.
func TestBuildTag_ExcludesThisPackageButNotItsReflector(t *testing.T) {
	deps := untaggedDeps(t, productionBinary)

	// Fixture sanity before either assertion: an empty or truncated dependency
	// set would satisfy the first check for entirely the wrong reason.
	require.Contains(t, deps, "github.com/modelcontextprotocol/go-sdk/mcp",
		"the dependency listing did not reach the MCP SDK, so neither assertion below means anything")

	require.False(t, slices.Contains(deps, schemagenImport),
		"%s is linked into %s — the schemagen build tag no longer excludes it",
		schemagenImport, productionBinary)

	require.True(t, slices.Contains(deps, reflectorImport),
		"%s is no longer linked into %s — doc.go says the reflector reaches the binary through the MCP SDK regardless of this package's build tag, and that is now false",
		reflectorImport, productionBinary)
}

// untaggedDeps returns the transitive production import paths of pkg under the
// DEFAULT build context (no schemagen tag) — i.e. exactly what a shipped
// binary links.
func untaggedDeps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-json", pkg)
	cmd.Dir = moduleRoot(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "go list -deps -json %s failed: %s", pkg, stderr.String())

	var paths []string
	dec := json.NewDecoder(&stdout)
	for dec.More() {
		var p struct{ ImportPath string }
		require.NoError(t, dec.Decode(&p))
		paths = append(paths, p.ImportPath)
	}
	require.NotEmpty(t, paths, "go list returned no packages")
	return paths
}

// moduleRoot resolves the module root from this file's COMPILED-IN source path
// rather than the process working directory, so the listing cannot silently be
// taken in some other tree (or a temp dir) and come back empty.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "cannot resolve this test's own source path")
	return filepath.Join(filepath.Dir(file), "..", "..")
}
