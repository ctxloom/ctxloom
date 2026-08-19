package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/trust"
)

// mustLocalRef mints a trust.BundleRef for a project-local bundle name, for
// tests that used to hand extractHooksFromBundle/extractMCPFromBundle/
// fragmentsFromBundle a bare source STRING (which the old
// trust.ItemRefFromSource round trip resolved to exactly this identity via
// ParseItemRef's bare-token fallback). Fails the test rather than silently
// minting a zero BundleRef on an unexpected error.
func mustLocalRef(t testing.TB, name string) trust.BundleRef {
	t.Helper()
	ref, err := trust.LocalRef(name)
	if err != nil {
		t.Fatalf("mustLocalRef(%q): %v", name, err)
	}
	return ref
}

// mustBuiltinRef is mustLocalRef's builtin-class counterpart, for tests that
// used to pass the old "builtin:<name>" source spelling.
func mustBuiltinRef(t testing.TB, name string) trust.BundleRef {
	t.Helper()
	ref, err := trust.BuiltinRef(name)
	if err != nil {
		t.Fatalf("mustBuiltinRef(%q): %v", name, err)
	}
	return ref
}
