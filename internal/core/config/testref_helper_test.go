package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// mustLocalRef mints an ident.BundleRef for a project-local bundle name, for
// tests that hand extractHooksFromBundle/extractMCPFromBundle/
// fragmentsFromBundle a local bundle. Fails the test rather than silently
// minting a zero BundleRef on an unexpected error.
func mustLocalRef(t testing.TB, name string) ident.BundleRef {
	t.Helper()
	ref, err := ident.LocalRef(name)
	if err != nil {
		t.Fatalf("mustLocalRef(%q): %v", name, err)
	}
	return ref
}
