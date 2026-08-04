package bundles_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// ProjectAuthoredRead is the ONE exported constructor that sets a read's trust
// axes. Two tests hold that exception in place: this one pins what it may say,
// and TestProjectAuthoredRead_ProductionCallSites pins who may say it.

func TestProjectAuthoredRead_SaysOnlyProjectLocalUnsigned(t *testing.T) {
	read := bundles.ProjectAuthoredRead("dev", &bundles.Bundle{Name: "dev"})

	assert.True(t, read.Claimed(), "it must establish every axis — a half-claimed read is withheld")
	assert.Equal(t, bundles.TrustCtxLocal, read.TrustCtx())
	assert.Equal(t, bundles.ProvenanceProject, read.Provenance)
	assert.Equal(t, bundles.SignatureNone, read.Signature(),
		"it may never claim a signature: there is no argument for one, and no bytes were verified")
	assert.Equal(t, bundles.SignerNone, read.Signer())
	assert.Empty(t, read.UntrustedSignerFingerprint())
}

// The call-site list, enforced. This constructor is a narrow exception to "no
// caller chooses provenance", and an exception nobody counts is a rule that
// erodes: a new production call site has to be a deliberate decision, not a
// convenient import.
//
// The legitimate cases are content that IS in this project's own tree but is not
// a bundle, so no Reader ever produced it: a profile's directly-declared
// executables, and a config.yaml MCP server.
func TestProjectAuthoredRead_ProductionCallSites(t *testing.T) {
	want := map[string]bool{
		// The declaration itself.
		"internal/bundles/reader_localfs.go": true,
		// A .ctxloom/profiles/<name>.yaml profile's inline hooks and MCP servers.
		"internal/lm/backends/managed.go": true,
		// A config.yaml MCP server, stamped for `ctxloom list --format json`.
		"internal/operations/trust.go": true,
	}

	root := repoRootFor(t)
	var found []string
	require.NoError(t, filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), "ProjectAuthoredRead") {
			rel, _ := filepath.Rel(root, path)
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	}))

	for _, f := range found {
		assert.True(t, want[f],
			"%s uses bundles.ProjectAuthoredRead. It states 'this content is project-authored' where nothing "+
				"verified it — the one place a caller may choose a provenance. Add it here only with a reason, "+
				"and prefer threading the READ from the loader that produced the content.", f)
	}
	for f := range want {
		assert.Contains(t, found, f, "expected call site %s no longer uses it — drop it from this list", f)
	}
}

// repoRootFor walks up from the working directory to the module root.
func repoRootFor(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for range 8 {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate the module root")
	return ""
}
