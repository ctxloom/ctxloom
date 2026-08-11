//go:build arch

// T12: engine identity was enumerated in (at least) four independently
// maintained rosters with four different memberships —
// internal/lm/grpc.RetiredScraperBackendNames, internal/operations'
// vendorReaderRegistry, internal/lm/isolation's composableEngines, and
// internal/lm/isolation's credentialSeedSpecs — and internal/operations (the
// ADR-0026 core) imported internal/claude/codex/kiro directly to branch on
// backend identity (hooks.go's checkHookTargetScope, delegate.go's
// resolveChatModel), a literal violation of the ports-and-adapters boundary
// docs/adr/0026-ports-and-adapters.md and docs/adr/0020-operations-llm-
// boundary.md already name: operations may depend only on the injected,
// polymorphic internal/lm/backends seam, never on a concrete engine package.
//
// Both defects are gated here, deliberately kept apart because they are
// different SHAPES of drift:
//
//   - TestArch_Operations_DoesNotImportEnginePlugins is the layering gate: it
//     re-catches the confirmed violation the moment a future change
//     reintroduces a direct internal/operations -> internal/{claude,codex,
//     kiro,opencode,acp} import edge, by the same AST-parse
//     technique TestArch_NonTestPackages_DoNotImportTestSupport already uses
//     in this package (production (non-_test.go) imports only, so a test
//     double importing an engine package for fixture purposes never trips
//     it).
//   - TestArch_EngineIdentityRosters_MembersAreRegisteredBackends is the
//     roster gate: each of the four rosters is a legitimately DIFFERENT
//     purpose-scoped subset of engines (which backend had a scraper worth
//     retiring; which backend has a vendor-native transcript to import from;
//     which backend has a known official container installer; which backend
//     has a generic host-credential seed spec) — collapsing them into one
//     flat list would be wrong, not a fix. What must never happen instead is
//     a roster naming a backend that ISN'T (or no longer is) a real,
//     registered internal/lm/backends name — a typo, or a stale entry left
//     behind when a backend was renamed or removed from the canonical
//     registry. This check names no engine (it reads backends.List() live,
//     the same way TestArch_ProtoConverters_MirrorEveryStructField in
//     internal/lm/grpc/arch_test.go names no struct field), so a new,
//     correctly-registered backend never requires an edit here — only a
//     roster member that has drifted out of registration does.
//
// Neither gate forces every roster to contain every registered backend: an
// engine legitimately absent from a roster (e.g. "acp" from
// vendorReaderRegistry — it has no vendor-native transcript store) is not a
// failure. The gate is a floor (every listed name is real), not a ceiling
// (every real name must be listed everywhere).
package arch

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// enginePluginImportPaths are the concrete, engine-identity-branching plugin
// packages ADR-0020/0026 reserve for internal/lm/backends (and each plugin's
// own family — see internal/acptest's sibling "one door" invariant for
// internal/acp specifically). Nothing else in the module's core may import
// them directly; internal/operations doing so for
// claude/codex/kiro was T12's confirmed violation.
var enginePluginImportPaths = []string{
	modulePath + "/internal/claude",
	modulePath + "/internal/codex",
	modulePath + "/internal/kiro",
	modulePath + "/internal/opencode",
	modulePath + "/internal/acp",
}

// TestArch_Operations_DoesNotImportEnginePlugins is the layering half of
// T12's fix: internal/operations (the ADR-0026 core) must depend only on the
// injected, polymorphic internal/lm/backends seam for anything
// engine-identity-shaped, never construct or branch on a concrete engine
// package itself. Scans production (non-_test.go) source only, via this
// package's own scan() (see arch_test.go) — a test fixture importing an
// engine package for setup purposes is not a layering violation.
func TestArch_Operations_DoesNotImportEnginePlugins(t *testing.T) {
	pkgs := scan(t)

	dirs := make([]string, 0, len(pkgs))
	for dir := range pkgs {
		if dir == "internal/operations" || strings.HasPrefix(dir, "internal/operations/") {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		t.Fatal("the scan found no internal/operations package(s) — the gate is looking at the wrong tree")
	}

	for _, dir := range dirs {
		for _, ip := range pkgs[dir].imports {
			if slices.Contains(enginePluginImportPaths, ip) {
				t.Errorf("package %s imports %s directly — internal/operations is the ADR-0026 core and may "+
					"only reach engine-identity-branching behavior through the injected internal/lm/backends "+
					"seam (see registry.go's agentDescriptor: resolveModel, hookGlobalScopePaths, and friends), "+
					"never by importing a concrete engine plugin package itself", dir, ip)
			}
		}
	}
}

// rosterCheck is one of T12's four engine-identity rosters: a named source
// (for error messages) and the backend names it currently lists.
type rosterCheck struct {
	source  string
	members []string
}

// TestArch_EngineIdentityRosters_MembersAreRegisteredBackends is the roster
// half of T12's fix: every name any of the four independently-maintained
// engine-identity rosters lists must be a real, CURRENTLY-registered
// internal/lm/backends name — never a typo, and never a stale reference left
// behind when a backend was renamed or removed from the canonical registry
// (internal/lm/backends/registry.go's descriptors table, the source of truth
// every one of these rosters is a purpose-scoped VIEW over, per
// docs/adr/0026-ports-and-adapters.md). Reads backends.List() live rather
// than naming backends here, so a new, correctly-registered backend never
// requires updating this test.
func TestArch_EngineIdentityRosters_MembersAreRegisteredBackends(t *testing.T) {
	known := backends.List()
	if len(known) == 0 {
		t.Fatal("backends.List() returned nothing — the canonical registry did not populate; the gate has " +
			"nothing to validate against")
	}

	rosters := []rosterCheck{
		{source: "internal/lm/grpc.RetiredScraperBackendNames", members: grpc.RetiredScraperBackendNames()},
		{source: "internal/operations.VendorReaderEngineNames (vendorReaderRegistry)", members: operations.VendorReaderEngineNames()},
		{source: "internal/lm/isolation.ComposableEngines (composableEngines)", members: isolation.ComposableEngines()},
		{source: "internal/lm/isolation.CredentialSeedEngineNames (credentialSeedSpecs)", members: isolation.CredentialSeedEngineNames()},
	}

	for _, r := range rosters {
		if len(r.members) == 0 {
			t.Errorf("%s reported zero members — either the roster is genuinely empty (update this test to say so "+
				"explicitly) or its accessor is broken", r.source)
			continue
		}
		for _, name := range r.members {
			if !slices.Contains(known, name) {
				t.Errorf("%s lists backend %q, which is not a currently-registered internal/lm/backends name "+
					"(known: %v) — a typo, or a stale entry from a rename/removal in the canonical registry",
					r.source, name, known)
			}
		}
	}
}
