//go:build arch

// T12: engine identity was enumerated in (at least) four independently
// maintained rosters with four different memberships —
// internal/lm/grpc.RetiredScraperBackendNames, internal/adapters/operations'
// vendorReaderRegistry, internal/adapters/isolation's composableEngines, and
// internal/adapters/isolation's credentialSeedSpecs — and internal/adapters/operations (the
// ADR-0026 core) imported concrete engine plugin packages directly to branch
// on backend identity (hooks.go's checkHookTargetScope, delegate.go's
// resolveChatModel), a literal violation of the ports-and-adapters boundary
// docs/adr/0026-ports-and-adapters.md and docs/adr/0020-operations-llm-
// boundary.md already name: operations may depend only on the injected,
// polymorphic engine registry, never on a concrete engine package.
//
// Both defects are gated here, deliberately kept apart because they are
// different SHAPES of drift:
//
//   - TestArch_Operations_DoesNotImportEnginePlugins is the layering gate: it
//     re-catches the confirmed violation the moment a future change
//     reintroduces a direct internal/adapters/operations -> engine-plugin import
//     edge (the packages enginePluginImportPaths names), by the same
//     AST-parse technique TestArch_NonTestPackages_DoNotImportTestSupport
//     already uses
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
//     registered composed engine name — a typo, or a stale entry left
//     behind when a backend was renamed or removed from the canonical
//     registry. This check names no engine (it reads operations.EngineNames() live,
//     the same way TestArch_ProtoConverters_MirrorEveryStructField in
//     internal/lm/grpc/arch_test.go names no struct field), so a new,
//     correctly-registered backend never requires an edit here — only a
//     roster member that has drifted out of registration does.
//
// The floor gate alone does not force every roster to contain every
// registered backend, and for a roster kept as a LITERAL TABLE it cannot: an
// engine legitimately absent (no vendor-native transcript store, say) and an
// engine somebody forgot to add are byte-identical there, and the miss is
// silent at the read site. A roster that is instead a DERIVED VIEW over the
// registry — the engine's own descriptor declares the fact, and the roster
// is the registry filtered by that declaration — makes the absence a stated
// value with a reason, and for those TestArch_DerivedEngineRosters_
// CoverEveryRegisteredBackend gates the reverse direction too: every
// registered backend is a member or says why it is not.
package arch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// enginePluginImportPaths are the concrete, engine-identity-branching plugin
// packages ADR-0020/0026 reserve for the engine registry (and each plugin's
// own family). Nothing else in the module's core may import them directly;
// internal/adapters/operations doing so was T12's confirmed violation.
var enginePluginImportPaths = []string{
	modulePath + "/internal/engines/claude",
}

// TestArch_Operations_DoesNotImportEnginePlugins is the layering half of
// T12's fix: internal/adapters/operations (the ADR-0026 core) must depend only on the
// composed engine registry (engines.Registry, agent.Hosted) for anything
// engine-identity-shaped, never construct or branch on a concrete engine
// package itself. Scans production (non-_test.go) source only, via this
// package's own scan() (see arch_test.go) — a test fixture importing an
// engine package for setup purposes is not a layering violation.
func TestArch_Operations_DoesNotImportEnginePlugins(t *testing.T) {
	pkgs := scan(t)

	dirs := make([]string, 0, len(pkgs))
	for dir := range pkgs {
		if dir == "internal/adapters/operations" || strings.HasPrefix(dir, "internal/adapters/operations/") {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		t.Fatal("the scan found no internal/adapters/operations package(s) — the gate is looking at the wrong tree")
	}

	for _, dir := range dirs {
		for _, ip := range pkgs[dir].imports {
			if slices.Contains(enginePluginImportPaths, ip) {
				t.Errorf("package %s imports %s directly — internal/adapters/operations is the ADR-0026 core and may "+
					"only reach engine-identity-branching behavior through the composed engine registry "+
					"(engines.Registry, the port and agent.Hosted), never by importing a concrete engine "+
					"plugin package itself", dir, ip)
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
// composed engine name — never a typo, and never a stale reference left
// behind when a backend was renamed or removed from the canonical registry
// (the composed engine registry, engines.Registry, the source of truth every
// one of these rosters is a purpose-scoped VIEW over, per
// docs/adr/0026-ports-and-adapters.md). Reads operations.EngineNames() live rather
// than naming backends here, so a new, correctly-registered backend never
// requires updating this test.
func TestArch_EngineIdentityRosters_MembersAreRegisteredBackends(t *testing.T) {
	known := operations.EngineNames()
	if len(known) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the canonical registry did not populate; the gate has " +
			"nothing to validate against")
	}

	rosters := []rosterCheck{
		{source: "internal/adapters/operations.VendorReaderEngineNames (vendorReaderRegistry)", members: operations.VendorReaderEngineNames()},
		{source: "internal/adapters/isolation.ComposableEngines (pushed engine.Descriptor.Container)", members: isolation.ComposableEngines()},
		{source: "internal/adapters/isolation.CredentialSeedEngineNames (pushed engine.Descriptor.Home.Credentials)", members: isolation.CredentialSeedEngineNames()},
	}

	for _, r := range rosters {
		if len(r.members) == 0 {
			t.Errorf("%s reported zero members — either the roster is genuinely empty (update this test to say so "+
				"explicitly) or its accessor is broken", r.source)
			continue
		}
		for _, name := range r.members {
			if !slices.Contains(known, name) {
				t.Errorf("%s lists backend %q, which is not a currently-registered composed engine name "+
					"(known: %v) — a typo, or a stale entry from a rename/removal in the canonical registry",
					r.source, name, known)
			}
		}
	}
}

// derivedRoster is a roster that is a VIEW over the registry: members are
// the registered backends whose descriptor provides the fact, and absence
// reads back the declared reason for every backend that is not a member.
type derivedRoster struct {
	source  string
	members []string
	absence func(name string) string
}

// credentialAbsence explains a registered engine outside the seeded roster:
// its Home relocates nothing, or declares its seed absent with a reason.
func credentialAbsence(name string) string {
	kind, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok {
		return ""
	}
	if !kind.Home().Relocates() {
		return name + " relocates no engine home: there is nothing to seed"
	}
	return kind.Home().Credentials.AbsentReason()
}

// transcriptAbsence explains a registered backend outside the vendor-reader
// roster: its kind supplies no readers (an empty slice, the port's absence).
func transcriptAbsence(name string) string {
	if _, ok := operations.VendorReaderAdaptersFor(name); ok {
		return ""
	}
	return name + " supplies no transcript readers (Engine.Transcripts is empty)"
}

// TestArch_DerivedEngineRosters_CoverEveryRegisteredBackend is the reverse of
// the floor above, for the rosters that are derived from the registry: every
// registered backend either appears in the roster or its descriptor declares
// the fact absent WITH A REASON. A forgotten entry cannot exist by
// construction; what this catches is the derivation itself dropping a member
// (a filter that skips an engine the descriptor provides for) — the
// mutation "remove an engine from the derived roster" dies here.
func TestArch_DerivedEngineRosters_CoverEveryRegisteredBackend(t *testing.T) {
	known := operations.EngineNames()
	if len(known) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the canonical registry did not populate; the gate has " +
			"nothing to validate against")
	}

	rosters := []derivedRoster{
		{
			source:  "internal/adapters/operations.VendorReaderEngineNames (Engine.Transcripts)",
			members: operations.VendorReaderEngineNames(),
			absence: transcriptAbsence,
		},
		{
			source:  "internal/adapters/isolation.AmbientSet (Engine.Home().Credentials)",
			members: seededEngines(),
			absence: credentialAbsence,
		},
		{
			source:  "internal/adapters/isolation.ComposableEngines (Engine.Container + Distribution)",
			members: isolation.ComposableEngines(),
			absence: containerAbsence(func(c engine.ContainerSpec, dist engine.Distribution) string {
				switch {
				case c.Install == nil:
					return "declares no container installer"
				case dist != engine.DistributionDefault:
					return "ships " + dist.String() + ", so it is not default-composed"
				}
				return ""
			}),
		},
		{
			source:  "internal/adapters/isolation.ContainerAuthEngines (Engine.Container + Distribution)",
			members: isolation.ContainerAuthEngines(),
			absence: containerAbsence(func(c engine.ContainerSpec, dist engine.Distribution) string {
				switch {
				case c.Auth.AbsentReason() != "":
					return c.Auth.AbsentReason()
				case dist == engine.DistributionTestOnly:
					return "a test double is never offered"
				}
				return ""
			}),
		},
	}

	for _, r := range rosters {
		for _, name := range known {
			member := slices.Contains(r.members, name)
			reason := r.absence(name)
			switch {
			case member && reason != "":
				t.Errorf("%s lists %q, whose descriptor declares the fact ABSENT (%q) — the derivation is not "+
					"reading the declaration", r.source, name, reason)
			case !member && reason == "":
				t.Errorf("%s omits registered backend %q, and its descriptor gives no reason for the absence — "+
					"either the derivation dropped a member, or the engine's declaration is not reaching the roster",
					r.source, name)
			}
		}
	}
}

// containerAbsence explains why a registered backend is outside a
// container roster: its kind refuses Container (that refusal), or the
// roster's own filter — capability or policy — excludes it, per why.
func containerAbsence(why func(engine.ContainerSpec, engine.Distribution) string) func(string) string {
	return func(name string) string {
		kind, ok := engines.Registry().Lookup(engine.Name(name))
		if !ok {
			return ""
		}
		c, err := kind.Container()
		if err != nil {
			return err.Error()
		}
		return why(c, kind.Root().Distribution)
	}
}

// seededEngines lists the backends isolation would seed credentials for —
// those with a PROVIDED seed at the seam, read the way CopyAmbient reads it.
func seededEngines() []string {
	var names []string
	for _, name := range isolation.AmbientEngineNames() {
		if isolation.AmbientSet(name) != nil {
			names = append(names, name)
		}
	}
	return names
}

// transcriptSchemaRelPath is the published canonical-transcript schema whose
// `engine` enum is one more engine-identity roster — the one external readers
// of a transcript see.
const transcriptSchemaRelPath = "docs/transcript.schema.json"

// TestArch_TranscriptSchemaEngineEnum_EqualsBackendRegistry holds the schema's
// `engine` enum to EQUALITY with operations.EngineNames(), not just the floor the
// rosters gate above applies. The recorder writes the registered backend name
// verbatim (internal/adapters/transcript.Record.Engine) and every registered backend
// reaches it (a oneshot run records under whatever `--llm` resolved to), so
// the set of names a transcript can carry IS the registry: a name in the enum
// that nothing registers admits fixtures no writer could produce, and a
// registered name missing from the enum makes a real transcript fail
// validation. Reads both sides live so neither a new backend nor a removal
// needs an edit here — only the schema does.
func TestArch_TranscriptSchemaEngineEnum_EqualsBackendRegistry(t *testing.T) {
	registered := operations.EngineNames()
	if len(registered) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the canonical registry did not populate; the gate has " +
			"nothing to validate against")
	}

	data, err := os.ReadFile(filepath.Join(moduleRoot(t), transcriptSchemaRelPath))
	if err != nil {
		t.Fatalf("read %s: %v", transcriptSchemaRelPath, err)
	}
	var schema struct {
		Properties struct {
			Engine struct {
				Enum []string `json:"enum"`
			} `json:"engine"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse %s: %v", transcriptSchemaRelPath, err)
	}
	enum := slices.Clone(schema.Properties.Engine.Enum)
	if len(enum) == 0 {
		t.Fatalf("%s declares no engine enum — the roster this gate checks is gone", transcriptSchemaRelPath)
	}

	sort.Strings(enum)
	if !slices.Equal(enum, registered) {
		t.Errorf("%s `engine` enum %v != operations.EngineNames() %v — the enum must name exactly the registered "+
			"backends: a member nothing registers admits fixtures no writer produces, and a registered "+
			"backend missing from it makes a real transcript fail validation",
			transcriptSchemaRelPath, enum, registered)
	}
}
