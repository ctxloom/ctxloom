package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// mustLocalRef mints an ident.BundleRef for a project-local bundle name, for
// tests that used to hand extractHooksFromBundle/extractMCPFromBundle/
// fragmentsFromBundle a bare source STRING (which the old
// ident.ItemRefFromSource round trip resolved to exactly this identity via
// the bare-token fallback). Fails the test rather than silently
// minting a zero BundleRef on an unexpected error.
func mustLocalRef(t testing.TB, name string) ident.BundleRef {
	t.Helper()
	ref, err := ident.LocalRef(name)
	if err != nil {
		t.Fatalf("mustLocalRef(%q): %v", name, err)
	}
	return ref
}
